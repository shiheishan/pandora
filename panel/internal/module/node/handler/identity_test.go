package handler

// 节点身份与安装令牌接口的 handler 测试：不连库，只测路径、校验、CA 未装配与重认证链。

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

func newIdentityEngine(reauth *poolReauthStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: reauth.handler(),
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).registerIdentity(e.Group("/v1"), kit)
	return e
}

func TestIdentityRoutesBadPathID(t *testing.T) {
	e := newIdentityEngine(&poolReauthStub{})
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/servers/not-a-uuid/bootstrap-token", ""},
		{http.MethodGet, "/v1/nodes/xyz/identity", ""},
		{http.MethodPost, "/v1/nodes/xyz/revoke-identity", ""},
	}
	for _, tc := range cases {
		w := doPool(e, tc.method, tc.path, tc.body)
		poolExpectErr(t, w, errs.NotFound, "")
	}
}

func TestServerBootstrapTokenTTLValidation(t *testing.T) {
	e := newIdentityEngine(&poolReauthStub{})
	sid := uuid.NewString()
	path := "/v1/servers/" + sid + "/bootstrap-token"

	w := doPool(e, http.MethodPost, path, `{"ttl_minutes":61}`)
	poolExpectErr(t, w, errs.ValidationFailed, "ttl_minutes")

	w = doPool(e, http.MethodPost, path, `{"ttl_minutes":-1}`)
	poolExpectErr(t, w, errs.ValidationFailed, "ttl_minutes")

	w = doPool(e, http.MethodPost, path, `{"ttl_minutes":30}`)
	poolExpectErr(t, w, errs.Unavailable, "")

	w = doPool(e, http.MethodPost, path, "")
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestNodeBootstrapTokenNeedsServer(t *testing.T) {
	e := newIdentityEngine(&poolReauthStub{})
	path := "/v1/nodes/bootstrap-token"

	w := doPool(e, http.MethodPost, path, `{"node_name":"n1","ttl_minutes":30}`)
	poolExpectErr(t, w, errs.Conflict, "")

	w = doPool(e, http.MethodPost, path, `{"node_name":"n1","server_id":"bad","ttl_minutes":30}`)
	poolExpectErr(t, w, errs.ValidationFailed, "server_id")

	w = doPool(e, http.MethodPost, path, `{"node_name":"n1","server_id":"`+uuid.NewString()+`","ttl_minutes":30}`)
	poolExpectErr(t, w, errs.Unavailable, "")
}

func TestIdentityRoutesReauth(t *testing.T) {
	stub := &poolReauthStub{deny: true}
	e := newIdentityEngine(stub)
	id := uuid.NewString()

	// GET identity 用不合法的 id：路径校验在 service 之前回 404（空 service 没有库，合法 id 会碰库）
	before := stub.calls
	w := doPool(e, http.MethodGet, "/v1/nodes/xyz/identity", "")
	if stub.calls != before {
		t.Fatalf("GET identity 不应要求重认证，calls %d -> %d", before, stub.calls)
	}
	poolExpectErr(t, w, errs.NotFound, "")

	for _, path := range []string{
		"/v1/servers/" + id + "/bootstrap-token",
		"/v1/nodes/bootstrap-token",
		"/v1/nodes/" + id + "/revoke-identity",
	} {
		body := ""
		if path == "/v1/nodes/bootstrap-token" {
			body = `{"node_name":"n","server_id":"` + id + `","ttl_minutes":30}`
		}
		w := doPool(e, http.MethodPost, path, body)
		poolExpectErr(t, w, errs.ReauthRequired, "")
	}
	if stub.calls != 3 {
		t.Fatalf("重认证应调用 3 次，实际 %d", stub.calls)
	}
}

func TestNodeIdentityResponseShape(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	keys, m := poolJSONKeys(t, nodeIdentityResponseFrom(service.NodeIdentityView{}))
	poolExpectKeys(t, keys, "bootstrap_tokens_pending", "identity")
	if m["identity"] != nil {
		t.Fatalf("空视图 identity 应为 null，得到 %#v", m["identity"])
	}
	if m["bootstrap_tokens_pending"] != float64(0) {
		t.Fatalf("pending 应为 0：%#v", m["bootstrap_tokens_pending"])
	}

	keys, m = poolJSONKeys(t, nodeIdentityResponseFrom(service.NodeIdentityView{
		PendingTokens: 2,
		Identity: &service.IdentityInfo{
			Seq:               3,
			Status:            "active",
			SPIFFEID:          "spiffe://pandora/server/abc",
			FingerprintSHA256: "deadbeef",
			IssuedAt:          now,
			ExpiresAt:         now.Add(24 * time.Hour),
		},
	}))
	poolExpectKeys(t, keys, "bootstrap_tokens_pending", "identity")
	idObj, _ := m["identity"].(map[string]any)
	poolExpectKeys(t, poolSortedKeys(idObj), "expires_at", "fingerprint_sha256", "issued_at", "serial", "spiffe_id", "status")
	if _, ok := idObj["revoked_at"]; ok {
		t.Fatal("未吊销不应带 revoked_at")
	}
	if _, ok := idObj["revoked_reason"]; ok {
		t.Fatal("未吊销不应带 revoked_reason")
	}
	if idObj["serial"] != float64(3) {
		t.Fatalf("serial 应为数字 3，得到 %#v", idObj["serial"])
	}

	revoked := now.Add(time.Hour)
	reason := "后台吊销"
	_, m = poolJSONKeys(t, nodeIdentityResponseFrom(service.NodeIdentityView{
		Identity: &service.IdentityInfo{
			Seq:               1,
			Status:            "revoked",
			SPIFFEID:          "spiffe://pandora/server/x",
			FingerprintSHA256: "aa",
			IssuedAt:          now,
			ExpiresAt:         now.Add(time.Hour),
			RevokedAt:         &revoked,
			RevokedReason:     &reason,
		},
	}))
	idObj, _ = m["identity"].(map[string]any)
	poolExpectKeys(t, poolSortedKeys(idObj),
		"expires_at", "fingerprint_sha256", "issued_at", "revoked_at", "revoked_reason", "serial", "spiffe_id", "status")
}

func TestBootstrapTokenResponseShape(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 8*3600))
	keys, _ := poolJSONKeys(t, bootstrapTokenResponseFrom(service.BootstrapTokenIssued{
		Token:          "tok",
		ExpiresAt:      now,
		InstallCommand: "curl ...",
	}))
	poolExpectKeys(t, keys, "expires_at", "install_command", "token")
}
