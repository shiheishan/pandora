package seed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// serverStatusGateway 模拟后台改服务器状态的接口：按后台的状态机校验每一步、核对行版本，
// 记下每台服务器走过的状态。
type serverStatusGateway struct {
	mu      sync.Mutex
	status  map[string]string
	version map[string]int64
	steps   map[string][]string
}

func (g *serverStatusGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	if r.URL.Path == "/v1/auth/login" {
		reply(http.StatusOK, map[string]any{"access_token": "tok"})
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/servers/"), "/status")
	var in struct {
		Status     string `json:"status"`
		RowVersion int64  `json:"row_version"`
	}
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/status") || json.NewDecoder(r.Body).Decode(&in) != nil {
		reply(http.StatusNotFound, map[string]any{"error": map[string]any{"code": "not_found"}})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if in.RowVersion != g.version[id] {
		reply(http.StatusConflict, map[string]any{"error": map[string]any{"code": "conflict", "message": "stale row_version"}})
		return
	}
	// 后台的服务器状态机（server_admin.go 的 serverStatusTransitions）里 ready 不能直接退役
	if g.status[id] == "ready" && in.Status == "retired" {
		reply(http.StatusConflict, map[string]any{"error": map[string]any{"code": "conflict", "message": "ready -> retired"}})
		return
	}
	g.status[id] = in.Status
	g.version[id]++
	g.steps[id] = append(g.steps[id], in.Status)
	reply(http.StatusOK, map[string]any{"id": id, "status": in.Status, "row_version": g.version[id]})
}

func TestRetireServersWalksEachServerToRetired(t *testing.T) {
	servers := []serverVersion{
		{ID: "s-ready", RowVersion: 3, Status: "ready"},
		{ID: "s-draft", RowVersion: 1, Status: "draft"},
		{ID: "s-draining", RowVersion: 7, Status: "draining"},
		{ID: "s-unhealthy", RowVersion: 2, Status: "unhealthy"},
	}
	gw := &serverStatusGateway{status: map[string]string{}, version: map[string]int64{}, steps: map[string][]string{}}
	for _, s := range servers {
		gw.status[s.ID], gw.version[s.ID] = s.Status, s.RowVersion
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 2, []string{"X-Real-IP"})
	ctx := context.Background()
	if err := p.login(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := retireServers(ctx, p, servers)
	if err != nil || n != len(servers) {
		t.Fatalf("retireServers = %d, %v", n, err)
	}
	want := map[string]string{"s-ready": "draining retired", "s-draft": "retired", "s-draining": "retired", "s-unhealthy": "retired"}
	for id, path := range want {
		if got := strings.Join(gw.steps[id], " "); got != path || gw.status[id] != "retired" {
			t.Fatalf("server %s walked %q, want %q", id, got, path)
		}
	}
	// 已全部退役：上一批为空时什么都不发
	if n, err := retireServers(ctx, p, nil); err != nil || n != 0 {
		t.Fatalf("empty retireServers = %d, %v", n, err)
	}
}

func TestRetireServersRefusesUnknownStatusBeforeAnyCall(t *testing.T) {
	gw := &serverStatusGateway{status: map[string]string{}, version: map[string]int64{}, steps: map[string][]string{}}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	p := newAdminPool(srv.URL, "admin@example.test", "not-a-secret", 0, 1, nil)
	if err := p.login(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := retireServers(context.Background(), p, []serverVersion{
		{ID: "s-ok", RowVersion: 1, Status: "draft"}, {ID: "s-odd", RowVersion: 1, Status: "teleporting"},
	})
	if err == nil || !strings.Contains(err.Error(), "s-odd") {
		t.Fatalf("unknown status: %v", err)
	}
	if len(gw.steps) != 0 {
		t.Fatalf("calls were made before validating every server: %v", gw.steps)
	}
}

// 服务器只按 loadtest- 名字圈、不碰已删除与已退役的；退役顺序是节点 draining → 服务器 → 节点 retired
// （节点是自己服务器的控制节点，服务器在役时批量退役会 409，10k-r1 第一次真用就撞上）。
func TestRetirePreviousRetiresServersBeforeNodes(t *testing.T) {
	for _, want := range []string{"name LIKE $2", "deleted_at IS NULL", "status <> 'retired'"} {
		if !strings.Contains(previousServersSQL, want) {
			t.Fatalf("previous servers query lacks %q", want)
		}
	}
	if strings.Contains(previousServersSQL, "DELETE") {
		t.Fatal("retiring must never delete servers")
	}
	block := sourcetest.Load(t, ".").Decl("retirePrevious")
	drain := strings.Index(block, `[]string{"active"}, "draining"`)
	servers := strings.Index(block, `retireServers(ctx, admin, servers)`)
	retire := strings.Index(block, `"disabled"}, "retired"`)
	if drain < 0 || servers <= drain || retire <= servers {
		t.Fatalf("retirement order drifted: drain=%d servers=%d retire=%d", drain, servers, retire)
	}
}
