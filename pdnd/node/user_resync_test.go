// [INPUT]: 依赖 panel 的 Client（兼容通道 UniProxy）与 StreamEvent，依赖 core 的 Core 抽象，依赖 net/http/httptest 起一个只认 ETag 的假面板
// [OUTPUT]: 对外提供 userTableCore、fakeUniProxy 两个夹具与入站重建后用户重同步的回归测试
// [POS]: pdnd/node 的用户镜像不变式守卫：内核用户表被清空之后，下一次拉用户必须是无条件的全量，轮询、事件流、增量三条路都要守住；config_rollback_test.go 守的是回滚本身
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// ---------------------------------------------------------------------------
// 夹具：会在入站重建时清空用户表的内核
// ---------------------------------------------------------------------------

// userTableCore 模拟真实内核最关键的一点：AddInbound / DelInbound 会把这个
// 入站上的用户表整个丢掉。rollbackCore 的 DelUsers 是空操作，看不出用户
// 有没有真的在内核里，所以这里单独做一份按 UUID 记账的。
type userTableCore struct {
	mu             sync.Mutex
	port           int
	users          map[string]core.User
	failSetRouting int
}

func newUserTableCore() *userTableCore {
	return &userTableCore{users: make(map[string]core.User)}
}

func (c *userTableCore) Type() string                                  { return "user-table-test" }
func (c *userTableCore) Start(context.Context) error                   { return nil }
func (c *userTableCore) Close() error                                  { return nil }
func (c *userTableCore) GetTraffic(string) ([]core.UserTraffic, error) { return nil, nil }
func (c *userTableCore) OnlineIPs(string) map[int64][]string           { return nil }

func (c *userTableCore) AddInbound(cfg *core.InboundConfig) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.port = cfg.Port
	c.users = make(map[string]core.User)
	return nil
}

func (c *userTableCore) DelInbound(string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.port = 0
	c.users = make(map[string]core.User)
	return nil
}

func (c *userTableCore) SetRouting(string, *core.Routing) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failSetRouting > 0 {
		c.failSetRouting--
		return errors.New("rejected routing")
	}
	return nil
}

func (c *userTableCore) AddUsers(_ string, users []core.User) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range users {
		c.users[u.UUID] = u
	}
	return nil
}

func (c *userTableCore) UpsertUsers(tag string, users []core.User) error {
	return c.AddUsers(tag, users)
}

func (c *userTableCore) DelUsers(_ string, uuids []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range uuids {
		delete(c.users, id)
	}
	return nil
}

// uuids 返回内核里此刻真实存在的用户，排好序便于比对。
func (c *userTableCore) uuids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.users))
	for id := range c.users {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// 夹具：只认 ETag 的假面板（UniProxy config / user 两个端点）
// ---------------------------------------------------------------------------

// fakeUniProxy 和真面板一样：请求带的 If-None-Match 等于当前 ETag 就回 304。
// 测试只改它的内容与 ETag，节点端走真实的 panel.Client。
type fakeUniProxy struct {
	mu         sync.Mutex
	port       int
	configETag string
	users      []map[string]any
	usersETag  string
	// userIfNoneMatch 按顺序记下每一次拉用户时带的 If-None-Match。
	userIfNoneMatch []string
	// configServed 记下回了 200 的配置请求次数。
	configServed int
}

func (p *fakeUniProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inm := r.Header.Get("If-None-Match")
	switch r.URL.Path {
	case "/api/v1/server/UniProxy/config":
		if inm != "" && inm == p.configETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		p.configServed++
		w.Header().Set("ETag", p.configETag)
		_ = json.NewEncoder(w).Encode(map[string]any{"server_port": p.port, "protocol": "vless"})
	case "/api/v1/server/UniProxy/user":
		p.userIfNoneMatch = append(p.userIfNoneMatch, inm)
		if inm != "" && inm == p.usersETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", p.usersETag)
		_ = json.NewEncoder(w).Encode(map[string]any{"users": p.users})
	default:
		http.NotFound(w, r)
	}
}

func (p *fakeUniProxy) setConfig(port int, etag string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.port, p.configETag = port, etag
}

func (p *fakeUniProxy) setUsers(etag string, uuids ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.usersETag = etag
	p.users = nil
	for i, id := range uuids {
		p.users = append(p.users, map[string]any{"id": i + 1, "uuid": id})
	}
}

func (p *fakeUniProxy) lastUserIfNoneMatch() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.userIfNoneMatch) == 0 {
		return "<无请求>"
	}
	return p.userIfNoneMatch[len(p.userIfNoneMatch)-1]
}

// newResyncFixture 起假面板与内核，并跑完第一轮同步：两个用户已在内核里。
func newResyncFixture(t *testing.T) (*Node, *userTableCore, *fakeUniProxy) {
	t.Helper()
	fake := &fakeUniProxy{}
	fake.setConfig(18080, `"cfg-1"`)
	fake.setUsers(`"users-1"`, "user-a", "user-b")
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	kernel := newUserTableCore()
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "token"})
	n := New(client, kernel, testLogger())

	n.syncOnce(context.Background())
	assertKernelUsers(t, kernel, "首轮同步后", "user-a", "user-b")
	// 第二轮什么都没变：两边都该换到 304，内核原样不动。
	n.syncOnce(context.Background())
	assertKernelUsers(t, kernel, "无变化的一轮之后", "user-a", "user-b")
	return n, kernel, fake
}

func assertKernelUsers(t *testing.T, kernel *userTableCore, when string, want ...string) {
	t.Helper()
	got := kernel.uuids()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("%s内核用户 = %v，期望 %v", when, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s内核用户 = %v，期望 %v", when, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 回归测试
// ---------------------------------------------------------------------------

// 只靠轮询（没有事件流）：面板改一次配置，下一次拉取之后用户必须全部回来。
//
// 入站重建会清空内核用户表；如果客户端还拿着旧的用户 ETag，面板回 304，
// syncUsers 当成「没变」直接返回——内核里一个用户都没有，直到面板那边的
// 用户集合恰好变一次。全站断线，面板上却一切正常。
func TestPolledConfigChangeResyncsUsers(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)

	fake.setConfig(18081, `"cfg-2"`) // 只改配置，用户集合不动
	n.syncOnce(context.Background())

	if got := fake.lastUserIfNoneMatch(); got != "" {
		t.Errorf("入站重建后拉用户仍带 If-None-Match=%s，期望无条件全量", got)
	}
	assertKernelUsers(t, kernel, "配置变更后的那一轮拉取之后", "user-a", "user-b")
}

// 事件流推来 sync.config：入站重建完要立刻补拉用户，不能等下一个轮询节拍。
//
// 否则从重建到下一次轮询（默认 60 秒）之间，内核里一个用户都没有。
func TestStreamedConfigChangeResyncsUsersImmediately(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)

	fake.setConfig(18081, `"cfg-2"`)
	n.applyStreamEvent(context.Background(), panel.StreamEvent{Type: panel.EventSyncConfig, ConfigETag: `"cfg-2"`})

	assertKernelUsers(t, kernel, "按事件应用配置之后", "user-a", "user-b")
}

// 入站重建之后，基于旧版本的增量不能打在空表上。
//
// 本地 userVersion 还停在重建前的那一版的话，增量的 FromVersion 恰好对得上，
// 补丁会被打在已经清空的内核上——结果只剩增量里新加的那几个人。
func TestDeltaAfterRebuildFallsBackToFullSync(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)
	ctx := context.Background()

	// 事件流先推一份全量，本地记下版本 users-1。
	n.applyStreamEvent(ctx, panel.StreamEvent{
		Type: panel.EventSyncUsers, Version: `"users-1"`,
		Users: []core.User{{ID: 1, UUID: "user-a"}, {ID: 2, UUID: "user-b"}},
	})
	// 入站重建（直接走 applyConfig，模拟重建后还没来得及补拉用户的窗口）。
	if err := n.applyConfig(map[string]any{"server_port": float64(18081), "protocol": "vless"}); err != nil {
		t.Fatal(err)
	}
	// 面板加了 user-c，推来基于 users-1 的增量。
	fake.setUsers(`"users-2"`, "user-a", "user-b", "user-c")
	n.applyStreamEvent(ctx, panel.StreamEvent{
		Type: panel.EventSyncUserDelta, FromVersion: `"users-1"`, ToVersion: `"users-2"`,
		Added: []core.User{{ID: 3, UUID: "user-c"}},
	})

	assertKernelUsers(t, kernel, "重建后收到旧基准增量之后", "user-a", "user-b", "user-c")
}

// 回滚也失败时，内核用户表处于未知状态：本地镜像、增量基准、用户 ETag
// 三份认知要一起作废，不能留下任何一份继续声称「内核里已经是这一版」。
func TestRollbackFailureResetsUserMirror(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)

	kernel.failSetRouting = 2 // 新配置被拒，恢复旧配置也被拒
	fake.setConfig(18081, `"cfg-2"`)
	if err := n.applyConfig(map[string]any{"server_port": float64(18081), "protocol": "vless"}); err == nil {
		t.Fatal("新配置与回滚都被拒绝，applyConfig 却报告成功")
	}
	if n.started {
		t.Fatal("回滚失败后节点仍标记为已启动")
	}
	if len(n.known) != 0 || n.userVersion != "" {
		t.Fatalf("回滚失败后用户镜像未作废：known=%d userVersion=%q", len(n.known), n.userVersion)
	}
	if _, changed, err := n.client.Users(context.Background()); err != nil || !changed {
		t.Fatalf("回滚失败后拉用户仍换回 304：changed=%v err=%v", changed, err)
	}
}
