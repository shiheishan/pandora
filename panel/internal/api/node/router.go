// Package node 实现 Node 域网关（EXT-001 令牌域之一）。
//
// 与其他三个域最大的不同：这里的调用方是机器不是人，因此没有会话、没有
// 用户令牌，身份完全由每个请求的 Ed25519 签名证明。Agent 的私钥在节点本地
// 生成且从不外传，平台只存公钥 —— 控制面被拖库也拿不到任何可冒充节点的材料。
package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Deps struct {
	Cfg  *config.Config
	Pool *db.Pool
	Log  *slog.Logger
	Node *nodefabric.Service
	// NodeStream 是面板推给节点的事件流。为 nil 时 /stream 端点返回错误，
	// 节点端回落到轮询——这条通路本来就是可选的加速。
	NodeStream *nodefabric.StreamHub
}

// 签名时间窗。太宽给重放留空间，太窄会被正常的时钟漂移误伤。
const signatureSkew = nodefabric.SignedRequestAcceptanceWindow

// healthResponse 是存活探针的响应，只报告进程还在。
type healthResponse struct {
	Status string `json:"status"`
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	// 访问日志与往返记账器（db_rt/kv_rt）：紧跟 RequestID，签名校验与 nonce 认领的往返都算进来。
	// 节点的例行请求每秒几百个，成功且不慢的降到 debug（分布看每分钟的 access_summary）
	r.Use(middleware.AccessLog(d.Log, middleware.QuietSuccess(20*time.Millisecond)))
	r.Use(middleware.ClientInfo)
	r.Use(middleware.Recovery(d.Log))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Timeout(20 * time.Second))
	r.Use(middleware.Tenant)

	h := &handlers{d: d}

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, healthResponse{Status: "ok"})
	})

	// --- UniProxy：Xboard / V2board 兼容协议 ---
	//
	// 放在 /api/v1 下而不是 /v1，是因为现成节点端把这个前缀写死了。
	// 认证走 query string 里的 token，与自研 Agent 的签名机制并行存在：
	// 两套节点端可以同时接入同一个平台，各用各的凭据。
	r.Route("/api/v1/server/UniProxy", func(r chi.Router) {
		r.Get("/config", h.uniConfig)
		r.Get("/user", h.uniUser)
		r.Post("/push", h.uniPush)
		r.Post("/alive", h.uniAlive)
		r.Post("/status", h.uniStatus)
		// 事件流：面板把配置和用户变动实时推下来，省掉轮询的等待。
		// 节点端连不上就继续轮询，功能不受影响。
		r.Get("/stream", h.uniStream)
	})

	r.Route("/v1", func(r chi.Router) {
		// 引导是唯一不需要节点签名的入口，身份由一次性令牌证明。
		r.Post("/nodes/bootstrap", h.legacyBootstrapDisabled)
		r.Post("/nodes/enrollments", h.beginEnrollment)
		r.Route("/nodes/enrollments/{enrollmentID}", func(r chi.Router) {
			r.Use(h.requireEnrollmentSignature)
			r.Get("/status", h.enrollmentStatus)
			r.Post("/commit", h.commitEnrollment)
			r.Post("/abort", h.abortEnrollment)
		})
		// 服务器级绑定（/v1/servers/*，见 server_router.go）
		registerServerRoutes(r, h)

		// 其余接口一律要求有效的节点签名
		r.Group(func(r chi.Router) {
			r.Use(h.requireNodeSignature(confirmInMiddleware))
			r.Get("/nodes/config-signing-key", h.fetchConfigSigningKey)
			r.Get("/nodes/config", h.fetchConfig)
			r.Post("/nodes/config/report", h.reportConfig)
		})
		// 两个最热的签名端点把身份复核并进自己的那一次查询（见 signedNode）：
		// 心跳的写入在 SQL 里以有效身份为门槛，拉生效配置的只读查询顺手读出纪元。
		r.Group(func(r chi.Router) {
			r.Use(h.requireNodeSignature(confirmInHandler))
			r.Post("/nodes/heartbeat", h.heartbeat)
			r.Get("/nodes/effective-config", h.fetchEffectiveConfig)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, d.Log, httpx.NotFoundOrForbidden())
	})
	return r
}

func (h *handlers) requireEnrollmentSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enrollmentID := chi.URLParam(r, "enrollmentID")
		nodeID := r.Header.Get("X-Node-Id")
		serialText := r.Header.Get("X-Node-Serial")
		ts := r.Header.Get("X-Node-Ts")
		nonce := r.Header.Get("X-Node-Nonce")
		sigText := r.Header.Get("X-Node-Sig")
		fail := func(reason string) {
			h.d.Log.Warn("enrollment request verification failed", "enrollment_id", enrollmentID,
				"node_id", nodeID, "reason", reason, "request_id", httpx.RequestIDFrom(r.Context()))
			httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "enrollment identity verification failed"))
		}
		serial, err := strconv.Atoi(serialText)
		if enrollmentID == "" || nodeID == "" || serial <= 0 || ts == "" || nonce == "" || sigText == "" {
			fail("missing signature headers")
			return
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil || ts != t.UTC().Format(time.RFC3339) {
			fail("invalid timestamp")
			return
		}
		now := time.Now().UTC()
		if d := now.Sub(t); d > signatureSkew || d < -signatureSkew {
			fail("timestamp outside acceptance window")
			return
		}
		nonceRaw, err := nodefabric.DecodeNodeRequestNonce(nonce)
		if err != nil {
			fail("invalid nonce")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			fail("request body read failed")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		bodyHash := sha256.Sum256(body)
		cred, err := h.d.Node.LookupEnrollmentCredential(r.Context(), httpx.TenantIDFrom(r.Context()), enrollmentID)
		var apiErr *httpx.Error
		if err != nil && !errors.As(err, &apiErr) {
			// 库不可用不是「凭据不对」：回 503 让节点稍后再来
			h.failNodeAuth(w, r, nodeID, fmt.Errorf("%w: %w", nodefabric.ErrNodeAuthUnavailable, err))
			return
		}
		if err != nil || cred.NodeID != nodeID || cred.Serial != serial {
			fail("enrollment credential mismatch")
			return
		}
		sig, err := base64.StdEncoding.DecodeString(sigText)
		if err != nil || !crypto.Verify(cred.PublicKey,
			nodefabric.CanonicalEnrollmentRequestV1(r.Method, r.URL.Path, enrollmentID, nodeID, serial, ts, nonce, bodyHash[:]), sig) {
			fail("signature mismatch")
			return
		}
		fingerprint := sha256.Sum256(nodefabric.CanonicalEnrollmentRequestV1(
			r.Method, r.URL.Path, enrollmentID, nodeID, serial, ts, nonce, bodyHash[:]))
		if err := h.d.Node.ClaimSignedRequest(r.Context(), httpx.TenantIDFrom(r.Context()), nodeID,
			nonceRaw, fingerprint[:], t); err != nil {
			h.failNodeAuth(w, r, nodeID, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withNodeID(r.Context(), nodeID)))
	})
}

type ctxKey string

const (
	ctxNodeID     ctxKey = "node_id"
	ctxSignedNode ctxKey = "signed_node"
)

// confirmMode 决定缓存身份的纪元复核在哪里做。
type confirmMode int

const (
	// confirmInMiddleware：中间件拿到当前纪元复核完才放行（多数签名端点）。
	confirmInMiddleware confirmMode = iota
	// confirmInHandler：复核交给 handler 并进它自己那一次查询（心跳、拉生效配置）。
	// 这类 handler 必须在产生任何副作用、写出任何响应之前完成复核（signedNode）。
	confirmInHandler
)

// signedNode 是一次已验签、已认领 nonce 的节点请求，记着身份复核还欠不欠。
type signedNode struct {
	tenantID, nodeID string
	check            nodefabric.NodeSignatureCheck
	payload, sig     []byte
	// pending 为真时还没拿当前纪元复核过缓存身份。
	pending bool
}

func signedNodeFrom(ctx context.Context) *signedNode {
	v, _ := ctx.Value(ctxSignedNode).(*signedNode)
	return v
}

// confirm 用给定的当前纪元完成复核（纪元没前进就不碰库）。
func (s *signedNode) confirm(ctx context.Context, svc *nodefabric.Service, epoch int64) error {
	if s == nil || !s.pending {
		return nil
	}
	if err := svc.ConfirmNodeIdentity(ctx, s.tenantID, s.nodeID, s.check, epoch, s.payload, s.sig); err != nil {
		return err
	}
	s.pending = false
	return nil
}

// confirmNow 自己读一次当前纪元来完成复核（没有别的查询可搭时）。
func (s *signedNode) confirmNow(ctx context.Context, svc *nodefabric.Service) error {
	if s == nil || !s.pending {
		return nil
	}
	epoch, err := svc.CurrentDeliveryEpoch(ctx, s.tenantID)
	if err != nil {
		return err
	}
	return s.confirm(ctx, svc, epoch)
}

// failNodeAuth 把身份环节的错误写成响应：后端不可用（库、Valkey、超时）回 503，
// 其余（身份无效、签名不匹配、重放）一律同一个 401，不告诉调用方是哪一种。
// pdnd 把 401 当永久失败，库抖一下就回 401 会让它的回执永久作罢（审计 node-panel-link #6）。
func (h *handlers) failNodeAuth(w http.ResponseWriter, r *http.Request, nodeID string, err error) {
	reason := "身份不存在或已吊销"
	switch {
	case errors.Is(err, nodefabric.ErrNodeAuthUnavailable), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		h.d.Log.Warn("节点请求验签暂不可用", "node_id", nodeID,
			"request_id", httpx.RequestIDFrom(r.Context()), "error", err.Error())
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnavailable, "节点认证服务暂不可用").WithInternal(err))
		return
	case errors.Is(err, nodefabric.ErrNodeSignatureMismatch):
		reason = "签名不匹配"
	case !errors.Is(err, nodefabric.ErrNodeIdentityInvalid):
		reason = "nonce 已用过或无效"
	}
	h.d.Log.Warn("节点请求验签失败", "node_id", nodeID, "reason", reason,
		"request_id", httpx.RequestIDFrom(r.Context()))
	httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "节点身份校验失败"))
}

// requireNodeSignature 校验 Ed25519 请求签名。
//
// 覆盖 方法+路径+节点ID+时间戳+请求体哈希：少任何一项都会留下可乘之机 ——
// 不签路径就能把心跳的签名拿去调配置接口，不签请求体就能改上报内容。
func (h *handlers) requireNodeSignature(mode confirmMode) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.verifyNodeSignature(w, r, mode, next)
		})
	}
}

func (h *handlers) verifyNodeSignature(w http.ResponseWriter, r *http.Request, mode confirmMode, next http.Handler) {
	nodeID := r.Header.Get("X-Node-Id")
	ts := r.Header.Get("X-Node-Ts")
	nonce := r.Header.Get("X-Node-Nonce")
	sig := r.Header.Get("X-Node-Sig")

	fail := func(reason string) {
		h.d.Log.Warn("节点请求验签失败",
			"node_id", nodeID, "reason", reason,
			"request_id", httpx.RequestIDFrom(r.Context()))
		// 统一错误，不告诉调用方是签名错、过期还是节点不存在
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "节点身份校验失败"))
	}

	if nodeID == "" || ts == "" || nonce == "" || sig == "" {
		fail("缺少签名头")
		return
	}

	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		fail("时间戳格式非法")
		return
	}
	if ts != t.UTC().Format(time.RFC3339) {
		fail("timestamp is not canonical UTC seconds")
		return
	}
	now := time.Now().UTC()
	if d := now.Sub(t); d > signatureSkew || d < -signatureSkew {
		fail("时间戳超出允许窗口")
		return
	}
	nonceRaw, err := nodefabric.DecodeNodeRequestNonce(nonce)
	if err != nil {
		fail("invalid request nonce")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		fail("读取请求体失败")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // 供后续 handler 再读一次
	sum := sha256.Sum256(body)

	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		fail("签名不是合法 base64")
		return
	}
	// 身份（公钥）在 aegis-node 里按节点缓存，之后拿当前下发纪元复核：身份或节点状态
	// 改过（纪元前进）就回库重验，吊销下一次请求即生效；缓存里的旧公钥验不过时也回库
	// 再验一次，刚重新接入、换了钥匙的节点不会被挡住。
	tenantID := httpx.TenantIDFrom(r.Context())
	payload := nodefabric.CanonicalPayloadV2(r.Method, r.URL.Path, nodeID, ts, nonce, sum[:])
	check, err := h.d.Node.VerifyNodeRequestSignature(r.Context(), tenantID, nodeID, payload, raw)
	if err != nil {
		h.failNodeAuth(w, r, nodeID, err)
		return
	}
	fingerprint := sha256.Sum256(payload)
	epoch, epochKnown, err := h.d.Node.ClaimSignedRequestNonce(r.Context(), tenantID, nodeID,
		nonceRaw, fingerprint[:], t)
	if err != nil {
		h.failNodeAuth(w, r, nodeID, err)
		return
	}
	signed := &signedNode{tenantID: tenantID, nodeID: nodeID, check: check,
		payload: payload, sig: raw, pending: check.NeedsEpoch()}
	switch {
	case epochKnown:
		// nonce 落在 PG 时，认领那条语句顺手读出了纪元
		err = signed.confirm(r.Context(), h.d.Node, epoch)
	case mode == confirmInMiddleware:
		err = signed.confirmNow(r.Context(), h.d.Node)
	}
	if err != nil {
		h.failNodeAuth(w, r, nodeID, err)
		return
	}
	ctx := context.WithValue(withNodeID(r.Context(), nodeID), ctxSignedNode, signed)
	next.ServeHTTP(w, r.WithContext(ctx))
}
