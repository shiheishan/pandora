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
	"github.com/aegispanel/aegis/internal/platform/realtime"
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

// 缓存开着的网关：签名身份、令牌认证、用户集都走缓存；节点退役的库变更通知一到，
// 两种认证立刻失效，不用等 TTL。通知链路与生产一致：库触发器 → 库监听 → realtime
// → 作废订阅。
func checkCachedGatewayRevocation(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool,
	signer *platformcrypto.Signer, privateKey ed25519.PrivateKey, tenantID, nodeID, runtimeToken string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := realtime.NewHub(nil, logger)
	defer hub.Close()
	cached := nodefabric.NewService(app, signer)
	cached.SetReleaseBinding(nodefabric.ReleaseBinding{})
	cached.AttachRealtime(hub)
	cached.EnableNodeCaches()

	listenCtx, stopListen := context.WithCancel(ctx)
	realtime.StartDBListener(listenCtx, app.Pool, hub, logger)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		cached.RunNodeCacheInvalidation(listenCtx, tenantID, logger)
	}()
	defer func() {
		stopListen()
		<-watchDone
	}()
	waitForNodeChangeEvents(t, ctx, admin, hub, tenantID, nodeID)

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

	for i := 0; i < 2; i++ {
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

	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='retired' WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for heartbeat() != http.StatusUnauthorized {
		if time.Now().After(deadline) {
			t.Fatal("retired node's cached identity was still accepted 5s after the change notification")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _ := users(""); code != http.StatusUnauthorized {
		t.Fatalf("retired node's cached UniProxy token = %d, want 401", code)
	}
}

// waitForNodeChangeEvents 等库监听真正 LISTEN 上：反复碰一下节点行，直到管理频道
// 收到它的变更通知。之后的改动就不会落在监听建立之前的空窗里。
func waitForNodeChangeEvents(t *testing.T, ctx context.Context, admin *pgxpool.Pool, hub *realtime.Hub, tenantID, nodeID string) {
	t.Helper()
	events, unsubscribe := hub.Subscribe([]string{realtime.ChannelAdmin(tenantID)})
	defer unsubscribe()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := admin.Exec(ctx, `UPDATE nodes SET name=name WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID); err != nil {
			t.Fatal(err)
		}
		select {
		case ev := <-events:
			if table, _ := ev.Payload["table"].(string); table == "nodes" {
				return
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Fatal("database change listener never delivered a nodes notification")
}
