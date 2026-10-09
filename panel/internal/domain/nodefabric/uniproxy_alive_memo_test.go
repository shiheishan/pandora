package nodefabric

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

// 备忘与 SQL 的刷新粒度是同一个值。
func TestAliveMemoUsesTheSQLRefreshStep(t *testing.T) {
	if aliveRefresh != "2 minutes" || aliveRefreshEvery != 2*time.Minute {
		t.Fatalf("aliveRefresh %q and aliveRefreshEvery %s drifted apart", aliveRefresh, aliveRefreshEvery)
	}
}

// 整份都刚被刷新过才跳过；任何一行是新的、或到了刷新点，照旧写。
func TestAliveMemoSkipsOnlyFullyRefreshedReports(t *testing.T) {
	m := newAliveMemo()
	h := func(ip string) []byte { s := sha256.Sum256([]byte(ip)); return s[:] }
	t0 := time.Unix(1_700_000_000, 0)
	uids, hashes := []int64{1, 2}, [][]byte{h("a"), h("b")}
	if m.covers("t", "n", uids, hashes, t0) {
		t.Fatal("unseen node's report skipped")
	}
	// 写入只刷新了第 1 行（第 2 行认下了但没改写）：不能跳过
	m.record("t", "n", uids[:1], hashes[:1], t0)
	if m.covers("t", "n", uids, hashes, t0.Add(time.Minute)) {
		t.Fatal("report with a row of unknown freshness skipped")
	}
	m.record("t", "n", uids, hashes, t0.Add(time.Minute))
	if !m.covers("t", "n", uids, hashes, t0.Add(2*time.Minute)) {
		t.Fatal("fully refreshed report not skipped")
	}
	if m.covers("t", "n", append(uids, 3), append(hashes, h("c")), t0.Add(2*time.Minute)) {
		t.Fatal("report with a new device skipped")
	}
	if m.covers("t", "n", uids[:1], hashes[:1], t0.Add(3*time.Minute)) {
		t.Fatal("row past its refresh point skipped")
	}
	if m.covers("t", "other", uids, hashes, t0.Add(90*time.Second)) {
		t.Fatal("another node's report skipped on this node's memo")
	}
	// 记的时候丢掉本节点过了刷新点的旧行
	m.record("t", "n", []int64{9}, [][]byte{h("z")}, t0.Add(10*time.Minute))
	if m.size != 1 || len(m.nodes[hbKey("t", "n")]) != 1 {
		t.Fatalf("stale rows kept: size=%d", m.size)
	}
}

// 放行集合：同一个名单切片只建一次；内容相同的新切片另建（直查路径），判定一样。
func TestServedSetReusesSharedListOnly(t *testing.T) {
	svc := NewService(nil, nil)
	svc.EnableNodeCaches()
	users := []ProxyUser{{ID: 3}, {ID: 9}}
	a := svc.servedSet(users)
	b := svc.servedSet(users)
	if len(a) != 2 || fmt.Sprintf("%p", a) != fmt.Sprintf("%p", b) {
		t.Fatal("shared list rebuilt its set")
	}
	copyUsers := append([]ProxyUser(nil), users...)
	c := svc.servedSet(copyUsers)
	if fmt.Sprintf("%p", a) == fmt.Sprintf("%p", c) {
		t.Fatal("a different slice reused another list's set")
	}
	if _, ok := c[9]; !ok || len(c) != 2 {
		t.Fatalf("rebuilt set wrong: %v", c)
	}
	if empty := svc.servedSet(nil); len(empty) != 0 {
		t.Fatal("empty list must give an empty set")
	}
}
