package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 增量删除按 ID→UUID 表一次换算（原先每个删除 ID 扫一遍全表，±500 人比整体替换还慢）：
// 1 万人里删 500 人，内核与本地镜像删得一致，耗时在毫秒级。
func TestApplyUserDeltaRemovesByIndex(t *testing.T) {
	kernel := newUserTableCore()
	client := panel.New(panel.Options{BaseURL: "http://127.0.0.1:1", NodeID: "n1", NodeType: "vless", Token: "token"})
	n := New(client, kernel, testLogger())
	const total, removed = 10000, 500
	users := make([]core.User, total)
	for i := range users {
		users[i] = core.User{ID: int64(i + 1), UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)}
	}
	if err := n.applyUsers(users); err != nil {
		t.Fatal(err)
	}
	ev := panel.StreamEvent{Type: panel.EventSyncUserDelta}
	for i := 0; i < removed; i++ {
		ev.Removed = append(ev.Removed, int64(i+1))
	}
	start := time.Now()
	if err := n.applyUserDelta(ev); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("%d 人里删 %d 人：%s", total, removed, took)
	if len(n.known) != total-removed {
		t.Fatalf("本地镜像剩 %d 人，期望 %d", len(n.known), total-removed)
	}
	kernel.mu.Lock()
	left := len(kernel.users)
	kernel.mu.Unlock()
	if left != total-removed {
		t.Fatalf("内核剩 %d 人，期望 %d", left, total-removed)
	}
	if _, ok := n.known[users[0].ID]; ok {
		t.Fatal("被删的人还在镜像里")
	}
	if took > 200*time.Millisecond {
		t.Fatalf("增量删除耗时 %s，不应随删除数乘以总人数增长", took)
	}
}
