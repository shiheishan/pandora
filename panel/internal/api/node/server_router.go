package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 服务器级绑定的 /v1/servers/* 接口（合约 docs/server-binding-contract.md）。
//
// 除接入 begin 之外，每个请求都由服务器身份按 aegis-server-request-v1 签名（合约 §2）：
// 五个签名头齐全、时间戳规范且在 ±5 分钟内、请求目标规范、原像验签通过、nonce 认领成功，
// 才放行。任何一步失败对外都是同一个 401「服务器身份校验失败」，后端不可用回 503。
// P1 只有接入 S1；清单、名单、上报、事件流、解绑（S2–S7）在 P2 挂到同一个前缀下。

// registerServerRoutes 挂服务器级接口，在 NewRouter 的 /v1 路由组里调用。
func registerServerRoutes(r chi.Router, h *handlers) {
	// 接入 begin 不签名，身份由一次性绑定令牌证明
	r.Post("/servers/enrollments", h.beginServerEnrollment)
	r.Route("/servers/enrollments/{enrollmentID}", func(r chi.Router) {
		r.Use(h.requireServerEnrollmentSignature)
		r.Get("/status", h.serverEnrollmentStatus)
		r.Post("/commit", h.commitServerEnrollment)
		r.Post("/abort", h.abortServerEnrollment)
	})
}

// maxServerRequestBody 是服务器请求体的上限（合约 §0.4），与面板 httpx.DecodeJSON 一致。
const maxServerRequestBody = 1 << 20

// serverSignedRequest 是一次已按合约重建出原像、还没验签的服务器请求。
type serverSignedRequest struct {
	serverID string
	serial   int64
	ts       time.Time
	nonce    []byte
	preimage []byte
	sig      []byte
}

// errServerRequestMalformed 是签名头或请求目标不合规：对外与验签失败同一个 401。
var errServerRequestMalformed = errors.New("server request signature headers are malformed")

// parseServerSignedRequest 读签名头与请求体（读完放回 r.Body 供 handler 再读），按合约 §2.2
// 重建原像。只做格式与时间窗校验，验签与 nonce 由调用方交给 nodefabric。
func parseServerSignedRequest(r *http.Request, now time.Time) (*serverSignedRequest, string, error) {
	serverID := r.Header.Get("X-Server-Id")
	serialText := r.Header.Get("X-Server-Serial")
	ts := r.Header.Get("X-Server-Ts")
	nonce := r.Header.Get("X-Server-Nonce")
	sigText := r.Header.Get("X-Server-Sig")
	if serverID == "" || serialText == "" || ts == "" || nonce == "" || sigText == "" {
		return nil, "missing signature headers", errServerRequestMalformed
	}
	serial, err := strconv.ParseInt(serialText, 10, 32)
	if err != nil || serial <= 0 || strconv.FormatInt(serial, 10) != serialText {
		return nil, "invalid serial", errServerRequestMalformed
	}
	if bindingcontract.CheckRequestTimestamp(ts) != nil {
		return nil, "invalid timestamp", errServerRequestMalformed
	}
	t, _ := time.Parse(time.RFC3339, ts)
	if d := now.Sub(t); d > bindingcontract.RequestSkew || d < -bindingcontract.RequestSkew {
		return nil, "timestamp outside acceptance window", errServerRequestMalformed
	}
	nonceRaw, err := nodefabric.DecodeNodeRequestNonce(nonce)
	if err != nil {
		return nil, "invalid nonce", errServerRequestMalformed
	}
	sig, err := base64.StdEncoding.DecodeString(sigText)
	if err != nil || len(sig) != 64 || base64.StdEncoding.EncodeToString(sig) != sigText {
		return nil, "invalid signature encoding", errServerRequestMalformed
	}
	// 合约 §2.3：用转义后的路径原样拼回查询串；不规范的目标在原像函数里被拒
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		target += "?" + r.URL.RawQuery
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxServerRequestBody+1))
	if err != nil {
		return nil, "request body read failed", errServerRequestMalformed
	}
	if len(body) > maxServerRequestBody {
		return nil, "request body too large", errServerRequestMalformed
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	preimage, err := bindingcontract.ServerRequestPreimage(bindingcontract.ServerRequestFields{
		Method: r.Method, Target: target, TenantID: httpx.TenantIDFrom(r.Context()),
		ServerID: serverID, Serial: serial, Timestamp: ts, Nonce: nonce,
		ReportID: r.Header.Get("X-Report-Id"), BodySHA256: bindingcontract.BodySHA256(body),
	})
	if err != nil {
		return nil, "non-canonical request: " + err.Error(), errServerRequestMalformed
	}
	return &serverSignedRequest{serverID: serverID, serial: serial, ts: t, nonce: nonceRaw,
		preimage: preimage, sig: sig}, "", nil
}

// failServerAuth 把服务器验签环节的错误写成响应：后端不可用回 503，其余一律同一个 401。
func (h *handlers) failServerAuth(w http.ResponseWriter, r *http.Request, serverID, reason string, err error) {
	if errors.Is(err, nodefabric.ErrServerAuthUnavailable) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		h.d.Log.Warn("服务器请求验签暂不可用", "server_id", serverID,
			"request_id", httpx.RequestIDFrom(r.Context()), "error", err.Error())
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnavailable, "服务器认证服务暂不可用").WithInternal(err))
		return
	}
	if reason == "" {
		switch {
		case errors.Is(err, nodefabric.ErrServerSignatureMismatch):
			reason = "signature mismatch"
		case err != nil:
			reason = err.Error()
		}
	}
	h.d.Log.Warn("服务器请求验签失败", "server_id", serverID, "reason", reason,
		"request_id", httpx.RequestIDFrom(r.Context()))
	httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "服务器身份校验失败"))
}

// requireServerEnrollmentSignature 验接入 status / commit / abort 的签名（合约 §3.2）：
// 签名者必须是这次接入登记的候选身份；接入已 commit 时改认有效服务器身份。
func (h *handlers) requireServerEnrollmentSignature(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, reason, err := parseServerSignedRequest(r, time.Now().UTC())
		if err != nil {
			h.failServerAuth(w, r, r.Header.Get("X-Server-Id"), reason, err)
			return
		}
		tenantID := httpx.TenantIDFrom(r.Context())
		if err := h.d.Node.VerifyServerEnrollmentRequest(r.Context(), tenantID, chi.URLParam(r, "enrollmentID"),
			req.serverID, req.serial, req.preimage, req.sig); err != nil {
			h.failServerAuth(w, r, req.serverID, "", err)
			return
		}
		fingerprint := sha256.Sum256(req.preimage)
		if err := h.d.Node.ClaimServerRequestNonce(r.Context(), tenantID, req.serverID, req.nonce,
			fingerprint[:], req.ts); err != nil {
			h.failServerAuth(w, r, req.serverID, "", err)
			return
		}
		next.ServeHTTP(w, r)
	})
}
