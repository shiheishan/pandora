package node

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 这些检查是 TestSignedNodeHTTPPG18 的一部分（effective 域的 -run 只列顶层函数名），
// 共用它接入好的节点。

// newSignedRequest 用固定的 nonce 与时间戳签一个请求，便于原样重放。
func newSignedRequest(t *testing.T, privateKey ed25519.PrivateKey, nodeID, method, target string,
	body []byte, nonce, ts string) *http.Request {
	t.Helper()
	sum := sha256.Sum256(body)
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	payload := nodefabric.CanonicalPayloadV2(method, req.URL.Path, nodeID, ts, nonce, sum[:])
	req.Header.Set("X-Node-Id", nodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Nonce", nonce)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)))
	return req
}

func freshNonce(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// 防重放：同一个签名请求（同一 nonce）原样再发一次必须 401。
func checkSignedReplayRejected(t *testing.T, privateKey ed25519.PrivateKey, nodeID, target string, body []byte) {
	t.Helper()
	nonce, ts := freshNonce(t), time.Now().UTC().Format(time.RFC3339)
	doRequest(t, newSignedRequest(t, privateKey, nodeID, http.MethodPost, target, body, nonce, ts), http.StatusOK, nil)
	doRequest(t, newSignedRequest(t, privateKey, nodeID, http.MethodPost, target, body, nonce, ts), http.StatusUnauthorized, nil)
}

// 节点带上手里的当前版：204、没有正文；带的是别的版本就照旧拿到全量。
func checkEffectiveConfigUnchanged(t *testing.T, privateKey ed25519.PrivateKey, nodeID, target string, current nodefabric.SignedConfig) {
	t.Helper()
	get := func(applied string, want int) *http.Response {
		t.Helper()
		req := newSignedRequest(t, privateKey, nodeID, http.MethodGet, target, nil,
			freshNonce(t), time.Now().UTC().Format(time.RFC3339))
		req.Header.Set(nodefabric.AppliedEffectiveReleaseHeader, applied)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("effective-config with applied=%q: status=%d want=%d body=%s", applied, resp.StatusCode, want, raw)
		}
		if want == http.StatusNoContent && len(raw) != 0 {
			t.Fatalf("unchanged effective-config carried a body: %s", raw)
		}
		return resp
	}
	get(current.ReleaseID+"/"+strconv.FormatUint(current.Generation, 10), http.StatusNoContent)
	get(uuid.NewString()+"/"+strconv.FormatUint(current.Generation, 10), http.StatusOK)
	get("not-a-release", http.StatusOK)
}

// 缓存开着的网关：签名身份与用户集走缓存，但改动提交后下一次请求就生效——下发纪元
// （迁移 00101）随 nonce 认领、UniProxy 认证一起读出，不靠事件、不等 TTL。
func checkCachedGatewayFollowsEpoch(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool,
	signer *platformcrypto.Signer, privateKey ed25519.PrivateKey, tenantID, nodeID, runtimeToken string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cached := nodefabric.NewService(app, signer)
	cached.SetReleaseBinding(nodefabric.ReleaseBinding{})
	cached.EnableNodeCaches()
	server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: logger, Node: cached}))
	defer server.Close()

	heartbeat := func() int {
		t.Helper()
		req := newSignedRequest(t, privateKey, nodeID, http.MethodPost, server.URL+"/v1/nodes/heartbeat",
			[]byte(`{"agent_version":"e2e-cache"}`), freshNonce(t), time.Now().UTC().Format(time.RFC3339))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	userURL := fmt.Sprintf("%s/api/v1/server/UniProxy/user?node_id=%s&node_type=shadowsocks", server.URL, nodeID)
	users := func(etag string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, userURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+runtimeToken)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("ETag")
	}

	for i := 0; i < 3; i++ {
		if code := heartbeat(); code != http.StatusOK {
			t.Fatalf("cached gateway heartbeat #%d = %d", i, code)
		}
	}
	code, etag := users("")
	if code != http.StatusOK || etag == "" {
		t.Fatalf("cached gateway user list = %d etag=%q", code, etag)
	}
	if code, _ := users(etag); code != http.StatusNotModified {
		t.Fatalf("cached gateway user list with matching ETag = %d, want 304", code)
	}

	// 只改身份表的手工吊销：没有节点行变化、没有事件，下一次签名请求照样被拒。
	if _, err := admin.Exec(ctx, `UPDATE node_identities SET status='revoked', revoked_at=now(),
		revoked_reason='e2e cache revocation' WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active'`,
		tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	if code := heartbeat(); code != http.StatusUnauthorized {
		t.Fatalf("revoked identity still accepted by the cached gateway: %d", code)
	}
	// 停用节点：UniProxy 认证每次查库，下一次请求就 401。
	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='disabled' WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	if code, _ := users(""); code != http.StatusUnauthorized {
		t.Fatalf("disabled node's UniProxy token = %d, want 401", code)
	}
}
