package service

// 流量上报的校验、分类与折算（纯函数，不碰库）。

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// maxTrafficEntryBytes 是一条上报里单个用户单个方向的上限：链路 2 Gbps 跑满两个上报周期（120 秒）约 30 GB，超过的不可能来自正常节点。
const maxTrafficEntryBytes uint64 = 30_000_000_000

// ErrTrafficReportInvalid 是上报编号不合规（1–64 个 [A-Za-z0-9._:-]）。只有节点端缺陷才会出现；
// 会话层按 ErrTrafficReportConflict 同样处理（记日志、回 ack、不入账），否则节点会原样重发、死循环。
var ErrTrafficReportInvalid = errors.New("node: 流量上报编号不合规")

// validReportID 校验编号：1–64 个 [A-Za-z0-9._:-]（与迁移 00022 的 CHECK 相同）。
func validReportID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

// compareEntry 是条目的规范顺序：NodeID 字节序、UID、Upload、Download。
func compareEntry(a, b TrafficEntry) int {
	if c := bytes.Compare(a.NodeID[:], b.NodeID[:]); c != 0 {
		return c
	}
	switch {
	case a.UID != b.UID:
		return cmpOrder(a.UID < b.UID)
	case a.Upload != b.Upload:
		return cmpOrder(a.Upload < b.Upload)
	case a.Download != b.Download:
		return cmpOrder(a.Download < b.Download)
	}
	return 0
}

func cmpOrder(less bool) int {
	if less {
		return -1
	}
	return 1
}

// canonicalTraffic 把条目按规范顺序排好（返回新切片，不改入参），并算内容摘要：
// SHA-256( 依次每条 NodeID 16 字节 | UID 大端 8 字节 | Upload 大端 8 字节 | Download 大端 8 字节 )。
// 摘要与条目顺序无关：同一批条目重发时换了顺序也认作同一份。
func canonicalTraffic(entries []TrafficEntry) ([]TrafficEntry, [32]byte) {
	sorted := append([]TrafficEntry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return compareEntry(sorted[i], sorted[j]) < 0 })
	h := sha256.New()
	var buf [40]byte
	for _, e := range sorted {
		copy(buf[:16], e.NodeID[:])
		binary.BigEndian.PutUint64(buf[16:24], uint64(e.UID))
		binary.BigEndian.PutUint64(buf[24:32], e.Upload)
		binary.BigEndian.PutUint64(buf[32:40], e.Download)
		h.Write(buf[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sorted, sum
}

// archivedEntry 是留档的一条（node_traffic_reports.entries 的元素）。
type archivedEntry struct {
	NodeID   uuid.UUID `json:"node_id"`
	UID      int64     `json:"uid"`
	Upload   uint64    `json:"upload"`
	Download uint64    `json:"download"`
}

// archiveJSON 把排好的条目编成留档 JSON 数组（空切片编成 "[]"）。
func archiveJSON(entries []TrafficEntry) (string, error) {
	out := make([]archivedEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, archivedEntry(e))
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// maxInt64Dec 是 int64 上限的 decimal 形式。
var maxInt64Dec = decimal.NewFromInt(math.MaxInt64)

// billedBytes 按节点倍率折算计费字节：bytes × rate 向零截断；rate <= 0 为 0；超过 int64 上限封顶。用 decimal，不用浮点。
func billedBytes(bytes int64, rate decimal.Decimal) int64 {
	if bytes <= 0 || !rate.IsPositive() {
		return 0
	}
	d := decimal.NewFromInt(bytes).Mul(rate).Truncate(0)
	if d.GreaterThan(maxInt64Dec) {
		return math.MaxInt64
	}
	return d.IntPart()
}

// satAdd 返回 a+b（都 >= 0），超过 math.MaxInt64 时封顶。
func satAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// trafficKey 是一个节点上的一个用户。
type trafficKey struct {
	NodeID uuid.UUID
	UID    int64
}

// trafficUsage 是一个节点（或节点上一个用户）的实际字节与计费字节。
type trafficUsage struct {
	Upload, Download, Billed int64
}

// trafficTally 是一份上报分类后的结果。
type trafficTally struct {
	// Upload、Download 是合规条目（含名单外）的合计，饱和加法
	Upload, Download  int64
	Invalid, Unlisted int
	// Nodes 是合规条目按节点的合计（Billed 在 hourlyRows 里按已入账的用户行算）
	Nodes map[uuid.UUID]*trafficUsage
	// Users 是名单内、字节 > 0 的条目按 (节点, uid) 合并，Billed 已按节点倍率折算
	Users map[trafficKey]*trafficUsage
}

// tallyTraffic 分类一份上报：
//   - 不合规（Invalid）：节点不在 rates 里（不属于这台服务器）、UID <= 0、任一方向超过 maxTrafficEntryBytes；
//   - 合规的计入合计与节点合计；其中 m.Has(NodeID, UID) 为假的算名单外（Unlisted），不计费；
//   - 名单内且字节 > 0 的按 (节点, uid) 合并，每个键按 billedBytes(上行 + 下行, 节点倍率) 折算一次。
func tallyTraffic(entries []TrafficEntry, rates map[uuid.UUID]decimal.Decimal, m RosterMembership) trafficTally {
	t := trafficTally{Nodes: map[uuid.UUID]*trafficUsage{}, Users: map[trafficKey]*trafficUsage{}}
	for _, e := range entries {
		if _, ok := rates[e.NodeID]; !ok || e.UID <= 0 || e.Upload > maxTrafficEntryBytes || e.Download > maxTrafficEntryBytes {
			t.Invalid++
			continue
		}
		up, down := int64(e.Upload), int64(e.Download)
		t.Upload, t.Download = satAdd(t.Upload, up), satAdd(t.Download, down)
		n := t.Nodes[e.NodeID]
		if n == nil {
			n = &trafficUsage{}
			t.Nodes[e.NodeID] = n
		}
		n.Upload, n.Download = satAdd(n.Upload, up), satAdd(n.Download, down)
		if !m.Has(e.NodeID, e.UID) {
			t.Unlisted++
			continue
		}
		if up == 0 && down == 0 {
			continue
		}
		k := trafficKey{NodeID: e.NodeID, UID: e.UID}
		u := t.Users[k]
		if u == nil {
			u = &trafficUsage{}
			t.Users[k] = u
		}
		u.Upload, u.Download = satAdd(u.Upload, up), satAdd(u.Download, down)
	}
	for k, u := range t.Users {
		u.Billed = billedBytes(satAdd(u.Upload, u.Download), rates[k.NodeID])
	}
	return t
}

// sortedUserKeys 按 (UID, NodeID 字节序) 排好 Users 的键。
func (t trafficTally) sortedUserKeys() []trafficKey {
	keys := make([]trafficKey, 0, len(t.Users))
	for k := range t.Users {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].UID != keys[j].UID {
			return keys[i].UID < keys[j].UID
		}
		return bytes.Compare(keys[i].NodeID[:], keys[j].NodeID[:]) < 0
	})
	return keys
}

// charges 给 subscription 扣量：Users 的每个键一笔 {UID, Billed}（Billed 为 0 也给，好拿到 uid → 订阅的对应），按 (UID, NodeID) 排序。
func (t trafficTally) charges() []subsvc.TrafficCharge {
	out := make([]subsvc.TrafficCharge, 0, len(t.Users))
	for _, k := range t.sortedUserKeys() {
		out = append(out, subsvc.TrafficCharge{UID: k.UID, Bytes: t.Users[k].Billed})
	}
	return out
}

// hourlyRows 生成两张小时汇总的增量；subs 是 ChargeTraffic 返回的 uid → 订阅。
// 用户行：Users 里 uid 在 subs 里的键，按 (NodeID, SubscriptionID) 字节序排序；
// 节点行：Nodes 的每个节点，Billed = 该节点已入账用户行的 Billed 之和，三项都为 0 的跳过，按 NodeID 字节序排序。
func (t trafficTally) hourlyRows(subs map[int64]uuid.UUID) ([]repo.NodeHourAdd, []repo.UserHourAdd) {
	billedByNode := map[uuid.UUID]int64{}
	users := make([]repo.UserHourAdd, 0, len(t.Users))
	for _, k := range t.sortedUserKeys() {
		subID, ok := subs[k.UID]
		if !ok {
			continue
		}
		u := t.Users[k]
		users = append(users, repo.UserHourAdd{NodeID: k.NodeID, SubscriptionID: subID, Upload: u.Upload, Download: u.Download, Billed: u.Billed})
		billedByNode[k.NodeID] = satAdd(billedByNode[k.NodeID], u.Billed)
	}
	sort.Slice(users, func(i, j int) bool {
		if c := bytes.Compare(users[i].NodeID[:], users[j].NodeID[:]); c != 0 {
			return c < 0
		}
		return bytes.Compare(users[i].SubscriptionID[:], users[j].SubscriptionID[:]) < 0
	})
	nodes := make([]repo.NodeHourAdd, 0, len(t.Nodes))
	for id, n := range t.Nodes {
		row := repo.NodeHourAdd{NodeID: id, Upload: n.Upload, Download: n.Download, Billed: billedByNode[id]}
		if row.Upload == 0 && row.Download == 0 && row.Billed == 0 {
			continue
		}
		nodes = append(nodes, row)
	}
	sort.Slice(nodes, func(i, j int) bool { return bytes.Compare(nodes[i].NodeID[:], nodes[j].NodeID[:]) < 0 })
	return nodes, users
}
