// Package node 实现 Node 域网关（EXT-001 四域之一）。
//
// 与其他三个域最大的不同：这里的调用方是机器不是人，因此没有会话、没有
// 用户令牌，身份完全由每个请求的 Ed25519 签名证明。Agent 的私钥在节点本地
// 生成且从不外传，平台只存公钥 —— 控制面被拖库也拿不到任何可冒充节点的材料。
package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.ClientInfo)
	r.Use(middleware.Recovery(d.Log))
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.Timeout(20 * time.Second))
	r.Use(middleware.Tenant)

	h := &handlers{d: d}

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.OK(w, map[string]string{"status": "ok"})
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

		// 其余接口一律要求有效的节点签名
		r.Group(func(r chi.Router) {
			r.Use(h.requireNodeSignature)
			r.Post("/nodes/heartbeat", h.heartbeat)
			r.Get("/nodes/config-signing-key", h.fetchConfigSigningKey)
			r.Get("/nodes/config", h.fetchConfig)
			r.Get("/nodes/effective-config", h.fetchEffectiveConfig)
			r.Post("/nodes/config/report", h.reportConfig)
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
			httpx.Fail(w, r, h.d.Log, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withNodeID(r.Context(), nodeID)))
	})
}

type ctxKey string

const ctxNodeID ctxKey = "node_id"

// requireNodeSignature 校验 Ed25519 请求签名。
//
// 覆盖 方法+路径+节点ID+时间戳+请求体哈希：少任何一项都会留下可乘之机 ——
// 不签路径就能把心跳的签名拿去调配置接口，不签请求体就能改上报内容。
func (h *handlers) requireNodeSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		id, err := h.d.Node.LookupIdentity(r.Context(), httpx.TenantIDFrom(r.Context()), nodeID)
		if err != nil {
			fail("身份不存在或已吊销")
			return
		}

		raw, err := base64.StdEncoding.DecodeString(sig)
		if err != nil {
			fail("签名不是合法 base64")
			return
		}
		payload := nodefabric.CanonicalPayloadV2(r.Method, r.URL.Path, nodeID, ts, nonce, sum[:])
		if !crypto.Verify(id.PublicKey, payload, raw) {
			fail("签名不匹配")
			return
		}
		fingerprint := sha256.Sum256(payload)
		if err := h.d.Node.ClaimSignedRequest(r.Context(), httpx.TenantIDFrom(r.Context()), nodeID,
			nonceRaw, fingerprint[:], t); err != nil {
			h.d.Log.Warn("node request nonce claim failed", "node_id", nodeID,
				"request_id", httpx.RequestIDFrom(r.Context()), "error", err)
			httpx.Fail(w, r, h.d.Log, err)
			return
		}

		next.ServeHTTP(w, r.WithContext(
			withNodeID(r.Context(), nodeID)))
	})
}
