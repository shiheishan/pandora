package node

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
)

// 入站没起来（首个发布就装不上）时，签名心跳如实报 degraded 并带原因，而不是
// 无条件 running——面板据此把健康分降到 40，而不是 90 照常进订阅。
func TestSignedHeartbeatReportsDegradedWithReason(t *testing.T) {
	w := newSignedWorld(t)
	kernel := &rejectingCore{userTableCore: newUserTableCore(), badPorts: map[int]bool{18080: true}}
	n := w.newNode(t, kernel)
	n.syncOnce(context.Background())
	if n.started {
		t.Fatal("坏端口居然装上了")
	}
	n.reportStatus(context.Background())
	delete(kernel.badPorts, 18080)
	n.syncOnce(context.Background())
	n.reportStatus(context.Background())

	w.mu.Lock()
	beats := append([]recordedHeartbeat(nil), w.heartbeats...)
	w.mu.Unlock()
	if len(beats) != 2 {
		t.Fatalf("收到 %d 次心跳，期望 2 次", len(beats))
	}
	if beats[0].body["runtime_status"] != runtimeDegraded || beats[0].reason != reasonNotStarted {
		t.Fatalf("没起来时心跳 %v 原因 %q，期望 degraded / %s", beats[0].body["runtime_status"], beats[0].reason, reasonNotStarted)
	}
	if _, leaked := beats[0].body["runtime_status_reason"]; leaked {
		t.Fatal("原因进了心跳正文：面板按 DisallowUnknownFields 解码，整条心跳会被 400")
	}
	if beats[1].body["runtime_status"] != runtimeRunning || beats[1].reason != "" {
		t.Fatalf("装上之后心跳 %v 原因 %q，期望 running 且无原因", beats[1].body["runtime_status"], beats[1].reason)
	}
}

// 兼容通道 /status 正文带上 runtime_status 与原因（面板与 Xboard 都不拒未知字段）。
func TestCompatStatusCarriesRuntimeStatus(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/UniProxy/status" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "token"})
	n := New(client, newUserTableCore(), testLogger())

	n.reportStatus(context.Background())
	if got["runtime_status"] != runtimeDegraded || got["runtime_status_reason"] != reasonNotStarted {
		t.Fatalf("未启动时 /status = %v", got)
	}
	n.started = true
	got = nil
	n.reportStatus(context.Background())
	if got["runtime_status"] != runtimeRunning {
		t.Fatalf("正常时 /status = %v", got)
	}
	if _, ok := got["runtime_status_reason"]; ok {
		t.Fatal("running 时不该带原因")
	}
}

// 回执补报持续失败（config/report 503）不挡用户同步：原先 syncOnce 在配置一出错
// 就返回，新用户一直同步不上、该删的人一直放行。
func TestUsersSyncWhileReceiptsFail(t *testing.T) {
	kernel := newUserTableCore()
	n, fake := newSignedFixture(t, kernel)
	fake.mu.Lock()
	fake.failReports = 1 << 20
	fake.mu.Unlock()
	ctx := context.Background()
	n.syncOnce(ctx) // 装上了，switched 回执 503
	fake.uni.setUsers(`"users-2"`, "user-a", "user-c")
	for i := 0; i < 3; i++ {
		n.syncOnce(ctx)
	}
	assertKernelUsers(t, kernel, "回执持续失败期间", "user-a", "user-c")
}

// 换钥检查失败（config-signing-key 503）不挡拉配置，更不挡用户同步。
func TestUsersAndConfigSyncWhileKeyCheckFails(t *testing.T) {
	w := newSignedWorld(t)
	kernel := newUserTableCore()
	n := w.newNode(t, kernel)
	keyFailing := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/nodes/config-signing-key" {
			http.Error(rw, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.srv.Config.Handler.ServeHTTP(rw, r)
	}))
	defer keyFailing.Close()
	identity := w.identity
	identity.Server = keyFailing.URL
	signed, err := panel.NewSignedClient(&identity)
	if err != nil {
		t.Fatal(err)
	}
	n.signed = signed
	n.client = panel.New(panel.Options{BaseURL: keyFailing.URL, NodeID: testNodeID, NodeType: "vless", Token: "token"})
	n.syncOnce(context.Background())
	if !n.started || kernel.port != 18080 {
		t.Fatalf("换钥检查失败挡住了拉配置：started=%v port=%d", n.started, kernel.port)
	}
	assertKernelUsers(t, kernel, "换钥检查失败时", "user-a", "user-b")
}
