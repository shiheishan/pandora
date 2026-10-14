package node

import (
	"fmt"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 增量删除按 ID→UUID 表一次换算（原先每个删除 ID 扫一遍全表，±500 人比整体替换还慢）：
// 1 万人里删 500 人，内核与本地镜像删得一致，耗时在毫秒级。
func TestApplyUserDeltaRemovesByIndex(t *testing.T) {
	kernel := newFakeKernel()
	ts := startedNode(t, kernel)
	n := ts.node(t, "n1")
	const total, removed = 10000, 500
	users := make([]core.User, total)
	for i := range users {
		users[i] = core.User{ID: int64(i + 1), UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)}
	}
	if err := n.applyUsers(users); err != nil {
		t.Fatal(err)
	}
	var gone []int64
	for i := 0; i < removed; i++ {
		gone = append(gone, int64(i+1))
	}
	start := time.Now()
	if err := n.applyUserDelta(nil, gone); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	t.Logf("%d 人里删 %d 人：%s", total, removed, took)
	if len(n.known) != total-removed {
		t.Fatalf("本地镜像剩 %d 人，期望 %d", len(n.known), total-removed)
	}
	left := len(kernel.uuids("n1"))
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
