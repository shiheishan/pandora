package service

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// tid 生成固定的 uuid，n 越大字节序越靠后。
func tid(n int) uuid.UUID {
	id := uuid.MustParse("00000000-0000-7000-8000-000000000000")
	id[15] = byte(n)
	return id
}

// fakeMembers 是 map 实现的名单成员判定。
type fakeMembers map[trafficKey]bool

func (f fakeMembers) Has(nodeID uuid.UUID, uid int64) bool {
	return f[trafficKey{NodeID: nodeID, UID: uid}]
}

func TestValidReportID(t *testing.T) {
	for _, c := range []struct {
		id   string
		want bool
	}{
		{"", false},
		{strings.Repeat("a", 65), false},
		{"has space", false},
		{"a/b", false},
		{"a", true},
		{strings.Repeat("Z", 64), true},
		{"r-1.2:3_x", true},
	} {
		if got := validReportID(c.id); got != c.want {
			t.Errorf("validReportID(%q) = %v", c.id, got)
		}
	}
}

func TestCanonicalTraffic(t *testing.T) {
	a := []TrafficEntry{
		{NodeID: tid(2), UID: 1, Upload: 5, Download: 6},
		{NodeID: tid(1), UID: 9, Upload: 1, Download: 2},
		{NodeID: tid(1), UID: 3, Upload: 7, Download: 0},
	}
	b := []TrafficEntry{a[2], a[0], a[1]}
	sa, da := canonicalTraffic(a)
	sb, db := canonicalTraffic(b)
	if da != db {
		t.Fatal("同一组条目换顺序，摘要应相同")
	}
	for i := range sa {
		if sa[i] != sb[i] {
			t.Fatalf("排序结果不同：%v %v", sa, sb)
		}
	}
	if sa[0].NodeID != tid(1) || sa[0].UID != 3 || sa[2].NodeID != tid(2) {
		t.Fatalf("排序 %v", sa)
	}
	if a[0].NodeID != tid(2) {
		t.Fatal("不该改入参的顺序")
	}
	c := append([]TrafficEntry(nil), a...)
	c[1].Download++
	if _, dc := canonicalTraffic(c); dc == da {
		t.Fatal("改一个字节数，摘要应不同")
	}
	j, err := archiveJSON(sa)
	if err != nil || !strings.HasPrefix(j, `[{"node_id":"`) || !strings.Contains(j, `"upload":7`) {
		t.Fatalf("留档 JSON %s %v", j, err)
	}
	if j, _ := archiveJSON(nil); j != "[]" {
		t.Fatalf("空留档应为 []，得 %s", j)
	}
}

func TestBilledBytes(t *testing.T) {
	for _, c := range []struct {
		bytes int64
		rate  string
		want  int64
	}{
		{100, "0", 0},
		{100, "1", 100},
		{3, "1.5", 4},
		{99, "0.01", 0},
		{1000, "2.33", 2330},
		{100, "-1", 0},
		{0, "1", 0},
		{math.MaxInt64, "2", math.MaxInt64},
	} {
		if got := billedBytes(c.bytes, decimal.RequireFromString(c.rate)); got != c.want {
			t.Errorf("billedBytes(%d, %s) = %d，期望 %d", c.bytes, c.rate, got, c.want)
		}
	}
}

func TestTallyTraffic(t *testing.T) {
	n1, n2, nx := tid(1), tid(2), tid(9)
	rates := map[uuid.UUID]decimal.Decimal{n1: decimal.RequireFromString("1"), n2: decimal.RequireFromString("1.5")}
	m := fakeMembers{{n1, 10}: true, {n2, 10}: true, {n1, 11}: true}
	entries := []TrafficEntry{
		{NodeID: n1, UID: 10, Upload: 100, Download: 200},
		{NodeID: n2, UID: 10, Upload: 1, Download: 0},
		{NodeID: n2, UID: 10, Upload: 0, Download: 1},           // 同一 (节点, uid) 合并：2 字节 × 1.5 = 3
		{NodeID: n2, UID: 11, Upload: 10, Download: 0},          // 名单外
		{NodeID: nx, UID: 10, Upload: 5, Download: 5},           // 节点不在这台服务器
		{NodeID: n1, UID: 0, Upload: 1, Download: 1},            // uid 非正
		{NodeID: n1, UID: -3, Upload: 1, Download: 1},           // uid 非正
		{NodeID: n1, UID: 11, Upload: maxTrafficEntryBytes + 1}, // 超上限
		{NodeID: n1, UID: 11, Upload: 0, Download: 0},           // 名单内但没有字节
	}
	tl := tallyTraffic(entries, rates, m)
	if tl.Invalid != 4 || tl.Unlisted != 1 {
		t.Fatalf("Invalid=%d Unlisted=%d", tl.Invalid, tl.Unlisted)
	}
	if tl.Upload != 111 || tl.Download != 201 {
		t.Fatalf("合计 %d/%d", tl.Upload, tl.Download)
	}
	if n := tl.Nodes[n2]; n == nil || n.Upload != 11 || n.Download != 1 {
		t.Fatalf("n2 合计 %+v", tl.Nodes[n2])
	}
	if _, ok := tl.Nodes[nx]; ok {
		t.Fatal("别的服务器的节点不该有合计")
	}
	if len(tl.Users) != 2 {
		t.Fatalf("Users %v", tl.Users)
	}
	if u := tl.Users[trafficKey{n2, 10}]; u == nil || u.Billed != 3 {
		t.Fatalf("(n2,10) 应合并后折算一次得 3：%+v", u)
	}
	if u := tl.Users[trafficKey{n1, 10}]; u == nil || u.Billed != 300 {
		t.Fatalf("(n1,10) %+v", u)
	}

	ch := tl.charges()
	if len(ch) != 2 || ch[0].UID != 10 || ch[0].Bytes != 300 || ch[1].Bytes != 3 {
		t.Fatalf("charges %v", ch)
	}

	sub10 := tid(100)
	nodes, users := tl.hourlyRows(map[int64]uuid.UUID{10: sub10})
	if len(users) != 2 || users[0].NodeID != n1 || users[0].SubscriptionID != sub10 || users[1].Billed != 3 {
		t.Fatalf("用户行 %v", users)
	}
	if len(nodes) != 2 || nodes[0].NodeID != n1 || nodes[0].Billed != 300 || nodes[1].Billed != 3 || nodes[1].Upload != 11 {
		t.Fatalf("节点行 %v", nodes)
	}

	// 找不到订阅的 uid：用户行不出，节点的计费不含它；三项全 0 的节点不出
	nodes, users = tl.hourlyRows(map[int64]uuid.UUID{})
	if len(users) != 0 || nodes[0].Billed != 0 || nodes[0].Upload != 100 {
		t.Fatalf("没有订阅时 %v %v", nodes, users)
	}
	empty := tallyTraffic([]TrafficEntry{{NodeID: n1, UID: 10}}, rates, m)
	if nodes, _ := empty.hourlyRows(nil); len(nodes) != 0 {
		t.Fatalf("全 0 的节点不该出行：%v", nodes)
	}
}

func TestSatAdd(t *testing.T) {
	if satAdd(math.MaxInt64-1, 5) != math.MaxInt64 || satAdd(2, 3) != 5 {
		t.Fatal("satAdd")
	}
}

// 同一 (节点, uid) 拆成多条、每条都在单条上限内，合并后超限：整键不计费、记一次不合规。
func TestTallyTrafficMergedCap(t *testing.T) {
	n1 := tid(1)
	rates := map[uuid.UUID]decimal.Decimal{n1: decimal.RequireFromString("1")}
	m := fakeMembers{{n1, 10}: true, {n1, 11}: true}
	half := maxTrafficEntryBytes/2 + 1
	tl := tallyTraffic([]TrafficEntry{
		{NodeID: n1, UID: 10, Upload: half},
		{NodeID: n1, UID: 10, Upload: half},
		{NodeID: n1, UID: 11, Download: 5},
	}, rates, m)
	if tl.Invalid != 1 {
		t.Fatalf("Invalid=%d，应为 1", tl.Invalid)
	}
	if _, ok := tl.Users[trafficKey{NodeID: n1, UID: 10}]; ok {
		t.Fatal("合并超限的键不应计费")
	}
	if u := tl.Users[trafficKey{NodeID: n1, UID: 11}]; u == nil || u.Billed != 5 {
		t.Fatalf("正常的键照扣：%+v", u)
	}
}
