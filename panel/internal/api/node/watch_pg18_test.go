package node

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 这个检查是 TestSignedNodeHTTPPG18 的一部分，共用它接入好的节点；必须排在
// checkCachedGatewayFollowsEpoch 之前（那一项会吊销身份、停用节点），这里的改动都会还原。

// watchedChangeDeadline 是「提交之后多久内必须看到」的上限。正常是通知送达（毫秒级）；
// 留足 CI 的调度抖动，但远小于任何 TTL（名单 5 秒、身份 10 分钟）。
const watchedChangeDeadline = 2 * time.Second

// 纪元监听开着的网关（w10quiet）：静默时心跳、204、304 不进库，但身份失效、节点退役在
// 提交后靠通知送达，下一次请求就生效；心跳合并之后照样落库。
func checkWatchedGatewayFollowsNotifications(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool,
	signer *platformcrypto.Signer, privateKey ed25519.PrivateKey, tenantID, nodeID, runtimeToken string,
	current nodefabric.SignedConfig) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	watched := nodefabric.NewService(app, signer)
	watched.SetReleaseBinding(nodefabric.ReleaseBinding{})
	watched.EnableNodeCaches()
	watchCtx, stopWatch := context.WithCancel(ctx)
	waitWatch := watched.StartEpochWatch(watchCtx, logger)
	waitHeartbeats := watched.StartHeartbeatCoalescer(watchCtx, logger)
	defer func() {
		stopWatch()
		waitWatch()
		waitHeartbeats()
	}()
	server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: logger, Node: watched}))
	defer server.Close()

	deadline := time.Now().Add(10 * time.Second)
	for !watched.EpochWatchHealthy() {
		if time.Now().After(deadline) {
			t.Fatal("epoch watch never became healthy against PG18")
		}
		time.Sleep(20 * time.Millisecond)
	}

	signed := func(method, path string, body []byte, header map[string]string) *http.Response {
		t.Helper()
		req := newSignedRequest(t, privateKey, nodeID, method, server.URL+path, body,
			freshNonce(t), time.Now().UTC().Format(time.RFC3339))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	status := func(resp *http.Response) int {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	heartbeat := func() int {
		t.Helper()
		return status(signed(http.MethodPost, "/v1/nodes/heartbeat", []byte(`{"agent_version":"e2e-watch"}`), nil))
	}
	applied := current.ReleaseID + "/" + strconv.FormatUint(current.Generation, 10)
	effective := func() int {
		t.Helper()
		return status(signed(http.MethodGet, "/v1/nodes/effective-config", nil,
			map[string]string{nodefabric.AppliedEffectiveReleaseHeader: applied}))
	}
	userURL := fmt.Sprintf("%s/api/v1/server/UniProxy/user?node_id=%s&node_type=shadowsocks", server.URL, nodeID)
	users := func(etag string, gzipOK bool) (int, string, *http.Response) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, userURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+runtimeToken)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if gzipOK {
			req.Header.Set("Accept-Encoding", "gzip")
		}
		resp, err := http.DefaultTransport.RoundTrip(req) // 不让 Transport 自动解压，看原样的正文
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Get("ETag"), resp
	}
	within := func(what string, cond func() bool) time.Duration {
		t.Helper()
		start := time.Now()
		for !cond() {
			if time.Since(start) > watchedChangeDeadline {
				t.Fatalf("%s: not visible %s after commit", what, watchedChangeDeadline)
			}
			time.Sleep(5 * time.Millisecond)
		}
		return time.Since(start)
	}

	// 预热：第一拍立即写，之后合并；204 与 304 照旧
	for i := 0; i < 3; i++ {
		if code := heartbeat(); code != http.StatusOK {
			t.Fatalf("watched heartbeat #%d = %d", i, code)
		}
	}
	if code := effective(); code != http.StatusNoContent {
		t.Fatalf("watched effective-config with the current release = %d, want 204", code)
	}
	code, etag, resp := users("", true)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if code != http.StatusOK || etag == "" || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("watched user list = %d etag=%q encoding=%q", code, etag, resp.Header.Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Users []nodefabric.ProxyUser `json:"users"`
	}
	if err := json.NewDecoder(zr).Decode(&list); err != nil || list.Users == nil {
		t.Fatalf("gzip user list does not decode: %v", err)
	}
	if code, _, resp := users(etag, false); code != http.StatusNotModified {
		t.Fatalf("watched user list with matching ETag = %d, want 304", code)
	} else {
		resp.Body.Close()
	}

	// 合并的心跳照样落库：批量写之后 last_heartbeat_at 跟上最后一拍
	before := time.Now()
	if code := heartbeat(); code != http.StatusOK {
		t.Fatalf("watched heartbeat = %d", code)
	}
	watched.FlushHeartbeats(ctx)
	var lastBeat time.Time
	if err := admin.QueryRow(ctx, `SELECT last_heartbeat_at FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID).Scan(&lastBeat); err != nil {
		t.Fatal(err)
	}
	if lastBeat.Before(before.Add(-time.Second)) {
		t.Fatalf("coalesced heartbeat not persisted: last_heartbeat_at=%s, beat at %s", lastBeat, before)
	}

	// 身份失效（只改身份表、没有任何节点行变化）：通知送达后下一次签名请求 401
	var expires time.Time
	if err := admin.QueryRow(ctx, `UPDATE node_identities SET expires_at = expires_at
		WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active' RETURNING expires_at`, tenantID, nodeID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE node_identities SET expires_at = now() - interval '1 second'
		WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active'`, tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	took := within("expired identity", func() bool { return heartbeat() == http.StatusUnauthorized })
	t.Logf("expired identity rejected %s after commit", took)
	if _, err := admin.Exec(ctx, `UPDATE node_identities SET expires_at = $3
		WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active'`, tenantID, nodeID, expires); err != nil {
		t.Fatal(err)
	}
	within("restored identity", func() bool { return heartbeat() == http.StatusOK })

	// 节点退出服务（服务状态没有触发器守着，测试里可以原样改回）：签名请求、生效配置、
	// UniProxy 名单都在通知送达后的下一次请求拒绝；改回之后恢复
	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='retired' WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	within("retired node's signed request", func() bool { return effective() == http.StatusUnauthorized })
	within("retired node's UniProxy token", func() bool {
		code, _, resp := users(etag, false)
		resp.Body.Close()
		return code == http.StatusUnauthorized
	})
	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='active' WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID); err != nil {
		t.Fatal(err)
	}
	within("reactivated node's 204", func() bool { return effective() == http.StatusNoContent })
	within("reactivated node's 304", func() bool {
		code, _, resp := users(etag, false)
		resp.Body.Close()
		return code == http.StatusNotModified
	})

	// 换 UniProxy 令牌（只改节点行的令牌列）：旧令牌在通知送达后的下一次请求 401
	var oldHash []byte
	if err := admin.QueryRow(ctx, `SELECT server_token_hash FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID).Scan(&oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE nodes SET server_token_hash=$3 WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID, platformcrypto.HashToken("rotated-"+runtimeToken)); err != nil {
		t.Fatal(err)
	}
	within("rotated UniProxy token", func() bool {
		code, _, resp := users(etag, false)
		resp.Body.Close()
		return code == http.StatusUnauthorized
	})
	if _, err := admin.Exec(ctx, `UPDATE nodes SET server_token_hash=$3 WHERE tenant_id=$1 AND id=$2::uuid`,
		tenantID, nodeID, oldHash); err != nil {
		t.Fatal(err)
	}
	within("restored UniProxy token", func() bool {
		code, _, resp := users(etag, false)
		resp.Body.Close()
		return code == http.StatusNotModified
	})
}
