package node

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// newServerSignedRequest 按合约 §2 签一个服务器请求；nonce 为空时现取一个。返回请求本身，便于原样重放。
func newServerSignedRequest(t *testing.T, priv ed25519.PrivateKey, base, tenantID, serverID string, serial int,
	method, target string, body []byte, nonce string) *http.Request {
	t.Helper()
	if nonce == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		nonce = base64.RawURLEncoding.EncodeToString(raw)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	pre, err := bindingcontract.ServerRequestPreimage(bindingcontract.ServerRequestFields{
		Method: method, Target: target, TenantID: tenantID, ServerID: serverID, Serial: int64(serial),
		Timestamp: ts, Nonce: nonce, BodySHA256: bindingcontract.BodySHA256(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, base+target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Server-Id", serverID)
	req.Header.Set("X-Server-Serial", strconv.Itoa(serial))
	req.Header.Set("X-Server-Ts", ts)
	req.Header.Set("X-Server-Nonce", nonce)
	req.Header.Set("X-Server-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(priv, pre)))
	return req
}

// TestServerEnrollmentHTTPPG18 走真实网关把服务器接入 S1 跑一遍：begin（令牌）→ 签名的 status /
// commit；同一个签名请求重放 401；换钥签名 401；后台解除绑定之后，已提交的接入再问状态也 401。
func TestServerEnrollmentHTTPPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SERVER_BINDING", DatabasePrefix: "pandora_server_binding",
		MarkerTable: "pandora_server_binding_test_marker", CommentTag: "pandora-server-binding-pg18",
	})
	seed := sha256.Sum256([]byte("server-enrollment-http-pg18"))
	signer, err := platformcrypto.NewSigner(seed[:])
	if err != nil {
		t.Fatal(err)
	}
	svc := nodefabric.NewService(app, signer)
	svc.SetReleaseBinding(nodefabric.ReleaseBinding{}) // 非生产：不比对发布产物
	server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Node: svc}))
	defer server.Close()

	const tenantID = middleware.DefaultTenantID
	var serverID string
	if err := admin.QueryRow(ctx, `INSERT INTO servers (tenant_id, name) VALUES ($1,$2) RETURNING id::text`,
		tenantID, "http-bind-"+uuid.NewString()).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	tok, err := svc.IssueServerBindingToken(ctx, tenantID, nodefabric.IssueServerBindingTokenInput{
		ServerID: serverID, PanelURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, 32)
	if _, err := rand.Read(enc); err != nil {
		t.Fatal(err)
	}
	beginBody := map[string]any{
		"token": tok.Token, "request_id": uuid.NewString(),
		"public_key": base64.StdEncoding.EncodeToString(pub), "enc_kem": bindingcontract.EncKEM,
		"enc_public_key": base64.StdEncoding.EncodeToString(enc), "agent_version": "v1.6.0",
		"features": []string{}, "capabilities": map[string]any{"protocols": []string{"vless"}},
		"hostname": "http-host", "cpu_cores": 1, "memory_mb": 1024, "disk_gb": 20,
	}
	// 未知字段拒收（合约 §0.4）
	withUnknown := map[string]any{"public_ipv4": "203.0.113.7"}
	for k, v := range beginBody {
		withUnknown[k] = v
	}
	doJSON(t, http.MethodPost, server.URL+"/v1/servers/enrollments", mustJSON(t, withUnknown), nil, http.StatusBadRequest, nil)

	var begun nodefabric.ServerEnrollmentOutput
	doJSON(t, http.MethodPost, server.URL+"/v1/servers/enrollments", mustJSON(t, beginBody), nil, http.StatusCreated, &begun)
	if begun.ServerID != serverID || begun.TenantID != tenantID || begun.Serial != 1 || begun.State != "pending" ||
		begun.ConfigKeyID != signer.KeyID() || begun.Features == nil {
		t.Fatalf("begin response: %+v", begun)
	}
	statusPath := "/v1/servers/enrollments/" + begun.EnrollmentID + "/status"

	var st nodefabric.ServerEnrollmentOutput
	nonce := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodGet, statusPath, nil, nonce),
		http.StatusOK, &st)
	if st.State != "pending" {
		t.Fatalf("status: %+v", st)
	}
	// 同一个 nonce 重放 401
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodGet, statusPath, nil, nonce),
		http.StatusUnauthorized, nil)
	// 别的钥匙签的、serial 对不上的都 401
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	doRequest(t, newServerSignedRequest(t, otherPriv, server.URL, tenantID, serverID, 1, http.MethodGet, statusPath, nil, ""),
		http.StatusUnauthorized, nil)
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 2, http.MethodGet, statusPath, nil, ""),
		http.StatusUnauthorized, nil)

	commitBody := mustJSON(t, map[string]any{
		"agent_version": "v1.6.0", "architecture": "amd64",
		"binary_sha256": strings.Repeat("1", 64), "config_sha256": strings.Repeat("2", 64),
		"unit_sha256": strings.Repeat("3", 64), "preflight_sha256": strings.Repeat("4", 64),
	})
	commitPath := "/v1/servers/enrollments/" + begun.EnrollmentID + "/commit"
	var committed nodefabric.ServerEnrollmentOutput
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodPost, commitPath, commitBody, ""),
		http.StatusOK, &committed)
	if committed.State != "committed" {
		t.Fatalf("commit: %+v", committed)
	}
	// 同一份证据重试幂等（换 nonce）
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodPost, commitPath, commitBody, ""),
		http.StatusOK, &committed)
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodGet, statusPath, nil, ""),
		http.StatusOK, &st)
	if st.State != "committed" {
		t.Fatalf("status after commit: %+v", st)
	}

	if _, err := svc.UnbindServer(ctx, tenantID, "", serverID); err != nil {
		t.Fatal(err)
	}
	doRequest(t, newServerSignedRequest(t, priv, server.URL, tenantID, serverID, 1, http.MethodGet, statusPath, nil, ""),
		http.StatusUnauthorized, nil)
}
