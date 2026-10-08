package node

import (
	"context"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// userPulls 返回假面板到目前为止收到的拉用户请求数。
func (p *fakeUniProxy) userPulls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.userIfNoneMatch)
}

// 轮询拉到的全量就是增量基准：面板随后推来基于这一版的增量，直接打上，不再回头
// 拉一次全量（nodeperf #14、10-08 验收问题 5：原先轮询不记版本，每条增量都报
// 「增量基准版本对不上，改拉全量」，1000 人的节点每次变更多一次整份名单往返）。
func TestPolledUsersBecomeDeltaBase(t *testing.T) {
	for _, etag := range []string{`"users-1"`, `W/"users-1"`} {
		t.Run(etag, func(t *testing.T) {
			n, kernel, fake := newResyncParts(t)
			fake.setUsers(etag, "user-a", "user-b")
			n.syncOnce(context.Background())
			assertKernelUsers(t, kernel, "首轮同步后", "user-a", "user-b")
			pulls := fake.userPulls()

			// 事件流里的版本不带 W/（nginx 只改写 REST 响应头）。
			fake.setUsers(`"users-2"`, "user-a", "user-b", "user-c")
			n.applyStreamEvent(context.Background(), panel.StreamEvent{
				Type: panel.EventSyncUserDelta, FromVersion: `"users-1"`, ToVersion: `"users-2"`,
				Added: []core.User{{ID: 3, UUID: "user-c"}},
			})
			assertKernelUsers(t, kernel, "增量之后", "user-a", "user-b", "user-c")
			if got := fake.userPulls(); got != pulls {
				t.Fatalf("增量基于轮询拉到的那一版，却又拉了 %d 次全量", got-pulls)
			}
			// 下一轮轮询带着增量的终点版本换 304。
			n.syncOnce(context.Background())
			if got := fake.lastUserIfNoneMatch(); got != `"users-2"` {
				t.Fatalf("增量之后轮询带的 If-None-Match = %s，期望 \"users-2\"", got)
			}
		})
	}
}

// 基准对不上的增量仍然改拉全量：手上是 users-1，增量却基于 users-0。
func TestDeltaFromUnknownBaseStillPullsFull(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)
	pulls := fake.userPulls()
	fake.setUsers(`"users-2"`, "user-a", "user-c")
	n.applyStreamEvent(context.Background(), panel.StreamEvent{
		Type: panel.EventSyncUserDelta, FromVersion: `"users-0"`, ToVersion: `"users-2"`,
		Added: []core.User{{ID: 3, UUID: "user-c"}},
	})
	if got := fake.userPulls(); got != pulls+1 {
		t.Fatalf("基准对不上应改拉一次全量，实际拉了 %d 次", got-pulls)
	}
	assertKernelUsers(t, kernel, "改拉全量之后", "user-a", "user-c")
}

// 轮询拉到的全量没装上：ETag 与增量基准一起作废。否则下一轮拿 ETag 换回 304，
// 这份名单永远装不上；增量也会被打在一份不完整的名单上。
func TestPolledUsersApplyFailureForgetsVersion(t *testing.T) {
	n, kernel, fake := newResyncFixture(t)
	fake.setUsers(`"users-2"`, "user-a", "user-b", "user-c")
	kernel.mu.Lock()
	kernel.failAddUsers = 1
	kernel.mu.Unlock()
	if err := n.syncUsers(context.Background()); err == nil {
		t.Fatal("内核拒收名单，syncUsers 却报告成功")
	}
	if n.userVersion != "" || n.client.UsersVersion() != "" {
		t.Fatalf("名单没装上，版本却留着：userVersion=%q etag=%q", n.userVersion, n.client.UsersVersion())
	}
	n.applyStreamEvent(context.Background(), panel.StreamEvent{
		Type: panel.EventSyncUserDelta, FromVersion: `"users-2"`, ToVersion: `"users-3"`,
		Added: []core.User{{ID: 4, UUID: "user-d"}},
	})
	if got := fake.lastUserIfNoneMatch(); got != "" {
		t.Fatalf("名单没装上之后应无条件拉全量，实际带 If-None-Match=%s", got)
	}
	assertKernelUsers(t, kernel, "重新拉全量之后", "user-a", "user-b", "user-c")
}
