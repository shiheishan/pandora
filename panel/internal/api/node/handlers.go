package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type handlers struct{ d Deps }

func withNodeID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxNodeID, id)
}
func nodeIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxNodeID).(string); ok {
		return v
	}
	return ""
}

func (h *handlers) bootstrap(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.BootstrapInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.Bootstrap(r.Context(), httpx.TenantIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("节点已引导接入",
		"node_id", out.NodeID, "serial", out.Serial,
		"request_id", httpx.RequestIDFrom(r.Context()))
	httpx.Created(w, out)
}

func (h *handlers) legacyBootstrapDisabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUpgradeRequired)
	_, _ = w.Write([]byte(`{"error":"legacy bootstrap is disabled; use /v1/nodes/enrollments"}`))
}

type beginEnrollmentReq struct {
	Token              string `json:"token"`
	NodeName           string `json:"node_name"`
	RequestID          string `json:"request_id"`
	PublicKey          string `json:"public_key"`
	RuntimeTokenSHA256 string `json:"runtime_token_sha256"`
	AgentVersion       string `json:"agent_version"`
	Hostname           string `json:"hostname"`
	CPUCores           int    `json:"cpu_cores"`
	MemoryMB           int    `json:"memory_mb"`
	DiskGB             int    `json:"disk_gb"`
	PublicIPv4         string `json:"public_ipv4"`
}

func readEnrollmentBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func (h *handlers) beginEnrollment(w http.ResponseWriter, r *http.Request) {
	body, err := readEnrollmentBody(w, r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "invalid enrollment request").WithInternal(err))
		return
	}
	var req beginEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	pub, pubErr := base64.StdEncoding.DecodeString(req.PublicKey)
	sig, sigErr := base64.StdEncoding.DecodeString(r.Header.Get("X-Enrollment-Signature"))
	hash := sha256.Sum256(body)
	if pubErr != nil || len(pub) != ed25519.PublicKeySize || sigErr != nil ||
		!ed25519.Verify(ed25519.PublicKey(pub), nodefabric.CanonicalEnrollmentBeginV1(r.URL.Path, req.RequestID, hash[:]), sig) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "enrollment proof is invalid"))
		return
	}
	out, err := h.d.Node.BeginEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), nodefabric.BeginEnrollmentInput{
		Token: req.Token, NodeName: req.NodeName, RequestID: req.RequestID, PublicKey: req.PublicKey,
		RuntimeTokenSHA256: req.RuntimeTokenSHA256, BeginRequestSHA256: hash[:], AgentVersion: req.AgentVersion,
		Hostname: req.Hostname, CPUCores: req.CPUCores, MemoryMB: req.MemoryMB, DiskGB: req.DiskGB, PublicIPv4: req.PublicIPv4,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

func (h *handlers) enrollmentStatus(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.GetEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "enrollmentID"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type commitEnrollmentReq struct {
	AgentVersion    string `json:"agent_version"`
	Architecture    string `json:"architecture"`
	BinarySHA256    string `json:"binary_sha256"`
	ConfigSHA256    string `json:"config_sha256"`
	UnitSHA256      string `json:"unit_sha256"`
	PreflightSHA256 string `json:"preflight_sha256"`
}

func (h *handlers) commitEnrollment(w http.ResponseWriter, r *http.Request) {
	body, err := readEnrollmentBody(w, r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var req commitEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	hash := sha256.Sum256(body)
	out, err := h.d.Node.CommitEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), nodefabric.CommitEnrollmentInput{
		EnrollmentID: chi.URLParam(r, "enrollmentID"), CommitRequestSHA256: hash[:],
		AgentVersion: req.AgentVersion, Architecture: req.Architecture, BinarySHA256: req.BinarySHA256,
		ConfigSHA256: req.ConfigSHA256, UnitSHA256: req.UnitSHA256, PreflightSHA256: req.PreflightSHA256,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type abortEnrollmentReq struct {
	Reason string `json:"reason"`
}

func (h *handlers) abortEnrollment(w http.ResponseWriter, r *http.Request) {
	var req abortEnrollmentReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.AbortEnrollment(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "enrollmentID"), req.Reason)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) heartbeat(w http.ResponseWriter, r *http.Request) {
	var in nodefabric.HeartbeatInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.Heartbeat(r.Context(),
		httpx.TenantIDFrom(r.Context()), nodeIDFrom(r.Context()), in)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) fetchConfigSigningKey(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.ConfigSigningKeyTransition(nodeIDFrom(r.Context()), r.Header.Get("X-Config-Key-Id"), time.Now())
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) fetchConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.d.Node.FetchConfig(r.Context(),
		httpx.TenantIDFrom(r.Context()), nodeIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, cfg)
}

func (h *handlers) fetchEffectiveConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.d.Node.FetchEffectiveConfig(r.Context(),
		httpx.TenantIDFrom(r.Context()), nodeIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, cfg)
}

type reportReq struct {
	Version       int    `json:"version"`
	ReportID      string `json:"report_id"`
	ReleaseID     string `json:"release_id"`
	Generation    uint64 `json:"generation"`
	ContentSHA256 string `json:"content_sha256"`
	Phase         string `json:"phase"`
	Detail        string `json:"detail"`
}

func (h *handlers) reportConfig(w http.ResponseWriter, r *http.Request) {
	var req reportReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var err error
	if req.ReleaseID != "" || req.ReportID != "" || req.Generation != 0 || req.ContentSHA256 != "" {
		err = h.d.Node.ReportEffectiveConfigApplied(r.Context(), httpx.TenantIDFrom(r.Context()), nodeIDFrom(r.Context()),
			nodefabric.EffectiveConfigReportInput{ReportID: req.ReportID, ReleaseID: req.ReleaseID,
				Generation: req.Generation, ContentHash: req.ContentSHA256, Phase: req.Phase, Detail: req.Detail})
	} else {
		err = h.d.Node.ReportConfigApplied(r.Context(), httpx.TenantIDFrom(r.Context()), nodeIDFrom(r.Context()),
			req.Version, req.Phase, req.Detail)
	}
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

//------------------------------------------------------------------------------
// UniProxy（Xboard / V2board 兼容）
//------------------------------------------------------------------------------

// authNode 优先使用 Authorization: Bearer，避免凭据进入 CDN、WAF 和
// 反向代理访问日志。query token 仅保留给尚未升级的 UniProxy 客户端。
// logProtocolDrift 在节点端自称的协议与面板不一致时记一条。
//
// 不是错误：管理员刚在面板上改过协议、节点端还没拉到新配置时就会这样，
// 下一轮同步就追上了。记它是为了让「改完协议之后节点行为不对」这类问题
// 有迹可循——否则只能靠猜。
func (h *handlers) logProtocolDrift(r *http.Request, n *nodefabric.ServingNode) {
	if n == nil || n.DeclaredType == "" {
		return
	}
	h.d.Log.Info("节点声明的协议与面板不一致，以面板为准",
		"node", n.ID, "节点端声明", n.DeclaredType, "面板记录", n.NodeType)
}

func (h *handlers) authNode(w http.ResponseWriter, r *http.Request) (*nodefabric.ServingNode, bool) {
	q := r.URL.Query()
	nodeID := q.Get("node_id")
	n, err := h.d.Node.AuthenticateNode(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodeID, uniProxyToken(r), q.Get("node_type"))
	if err != nil {
		h.d.Log.Warn("节点端认证失败",
			"node_id", q.Get("node_id"), "node_type", q.Get("node_type"),
			"request_id", httpx.RequestIDFrom(r.Context()))
		httpx.Fail(w, r, h.d.Log, err)
		return nil, false
	}
	h.logProtocolDrift(r, n)
	return n, true
}

func uniProxyToken(r *http.Request) string {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return r.URL.Query().Get("token")
	}
	// Any explicit Authorization header owns credential selection. Reject
	// empty, malformed, or repeated values instead of silently downgrading to
	// the legacy query credential and creating an ambiguous auth state.
	if len(values) != 1 {
		return ""
	}
	parts := strings.Fields(values[0])
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func (h *handlers) uniConfig(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	// 分流读取失败时必须 fail-closed。继续下发空路由会让节点把既有封禁
	// 或指定出口替换为默认直连；5xx 会让 QNode 保留上一份已验证配置。
	if outs, routes, rerr := h.d.Node.LoadRouting(r.Context(), httpx.TenantIDFrom(r.Context()), n.ID); rerr != nil {
		h.d.Log.Error("加载节点分流配置失败", "node", n.ID, "err", rerr)
		httpx.Fail(w, r, h.d.Log, httpx.Internal(rerr))
		return
	} else {
		n.Outbounds, n.Routes = outs, routes
	}

	body, etag, err := h.d.Node.BuildNodeConfig(n)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// 节点端靠 304 判断「配置没变」，省掉一次全量解析与内核重载
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(body)
}

func (h *handlers) uniUser(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	users, err := h.d.Node.ListNodeUsers(r.Context(), httpx.TenantIDFrom(r.Context()), n)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	// 用户列表绝大多数轮次是不变的，靠 ETag 让节点端拿 304 就走。
	//
	// 省掉的两头都不小：面板不用序列化整份列表，节点端不用解析再逐个
	// 比对。几十个用户时无所谓，几千个用户每 15 秒一次全量往返就很可观了。
	//
	// 和 /config 用同一套弱比较——中间的 nginx 一旦压缩响应就会把 ETag
	// 改写成 W/"..." 形式，字符串相等会永远不匹配。
	etag := nodefabric.UserSetVersion(users)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	// 字段名与结构必须与 UniProxy 一致，节点端按 users 数组解析
	httpx.OK(w, map[string]any{"users": users})
}

func (h *handlers) uniPush(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "读取上报失败"))
		return
	}
	res, err := h.d.Node.ReportTraffic(r.Context(), httpx.TenantIDFrom(r.Context()), n, raw)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if res.Duplicate {
		h.d.Log.Warn("丢弃重复的流量上报",
			"node", n.Name, "bytes", res.TotalBytes,
			"request_id", httpx.RequestIDFrom(r.Context()))
	}
	// 节点端只看 HTTP 状态码，返回体内容不影响它，但保留便于排查
	httpx.OK(w, map[string]any{"data": true, "accepted": res.Accepted})
}

func (h *handlers) uniAlive(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "读取上报失败"))
		return
	}
	cnt, err := h.d.Node.ReportAlive(r.Context(), httpx.TenantIDFrom(r.Context()), n, raw)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"data": true, "ips": cnt})
}

func (h *handlers) uniStatus(w http.ResponseWriter, r *http.Request) {
	n, ok := h.authNode(w, r)
	if !ok {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeBadRequest, "读取状态上报失败"))
		return
	}
	if err := h.d.Node.ReportRuntimeStatus(r.Context(), httpx.TenantIDFrom(r.Context()), n, raw); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"data": true})
}

// etagMatches 按 RFC 7232 的弱比较判断 If-None-Match。
//
// 必须弱比较，不能用字符串相等。原因是这条链路上有代理：
// nginx 一旦对响应做了 gzip，就会把 `"abc"` 改写成 `W/"abc"`
// （nginx 1.7.3 起的行为，因为压缩后的字节流不再与原文逐字节相同）。
// 而 Go 的 http.Transport 默认带 `Accept-Encoding: gzip`，
// 所以节点端拿到的永远是弱形式，回传的也是弱形式。
//
// 用字符串相等去比，结果是**永远不匹配**：面板每次都回 200 全量配置，
// 节点端每个拉取周期都重建一次入站、清空一次用户表再全量下发。
// 这个故障不会报错，只会安静地把「配置没变就什么都不做」变成
// 「每 15 秒把整个节点重来一遍」—— 实测确实如此。
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" || etag == "" {
		return false
	}
	// If-None-Match 允许逗号分隔的多个值，也允许 *
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if weakETag(candidate) == weakETag(etag) {
			return true
		}
	}
	return false
}

// weakETag 去掉 W/ 前缀，得到可比较的实体标签。
func weakETag(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "W/")
}
