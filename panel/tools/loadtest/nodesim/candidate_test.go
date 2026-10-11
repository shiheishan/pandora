package nodesim

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// 节点只记「可能连在自己身上」的那部分用户：报给面板的流量与在线名单必须和记着全员时一字不差。
func TestApplyUsersKeepsOnlyThisNodesCandidatesWithoutChangingReports(t *testing.T) {
	const total, index = 7, 3
	w := &workload{ratio: 0.5, trafficMiB: 1, total: total}
	var all []nodefabric.ProxyUser
	var wantIDs []int64
	for id := int64(1); id <= 5000; id++ {
		all = append(all, nodefabric.ProxyUser{ID: id, UUID: "u"})
		if w.candidate(id, index) {
			wantIDs = append(wantIDs, id)
		}
	}
	all = append(all, nodefabric.ProxyUser{ID: 9000, UUID: ""}) // 没有 UUID 的从来不进名单
	n := &simNode{index: index, work: w, obs: &observer{fleet: newFleetStats()}}
	n.applyUsers(all)
	if !slices.Equal(n.userIDs, wantIDs) {
		t.Fatalf("kept %d ids, want exactly the %d candidates in order", len(n.userIDs), len(wantIDs))
	}
	if len(n.userIDs) == 0 || len(n.userIDs) > 5000/total*2 {
		t.Fatalf("a node should keep roughly 1/%d of the users, kept %d of 5000", total, len(n.userIDs))
	}

	fullIDs := make([]int64, 0, len(all))
	for _, u := range all {
		if u.UUID != "" {
			fullIDs = append(fullIDs, u.ID)
		}
	}
	full, _ := w.trafficFor(fullIDs, index, rand.New(rand.NewPCG(1, 2)))
	kept, _ := w.trafficFor(n.userIDs, index, rand.New(rand.NewPCG(1, 2)))
	if len(full) == 0 || len(full) != len(kept) {
		t.Fatalf("traffic reports differ: full list %d users, kept list %d", len(full), len(kept))
	}
	for uid := range full {
		if _, ok := kept[uid]; !ok {
			t.Fatalf("user %s reports traffic with the full list but not the trimmed one", uid)
		}
	}
	if a, b := w.aliveFor(fullIDs, index), w.aliveFor(n.userIDs, index); len(a) != len(b) {
		t.Fatalf("alive reports differ: %d vs %d", len(a), len(b))
	}

	// 换了凭据的用户在增量里 Removed 与 Added 都有（面板 DiffUsers）：先删后加，人还在
	kept0 := n.userIDs[0]
	before := len(n.userIDs)
	n.applyUserDelta(streamEvent{Removed: []int64{kept0}, Added: []nodefabric.ProxyUser{{ID: kept0, UUID: "rotated"}}})
	if len(n.userIDs) != before || !slices.Contains(n.userIDs, kept0) {
		t.Fatalf("rotated user %d dropped from the list (%d -> %d users)", kept0, before, len(n.userIDs))
	}

	// 增量也只收候选
	n.applyUserDelta(streamEvent{Added: []nodefabric.ProxyUser{{ID: 100001, UUID: "u"}, {ID: 100002, UUID: "u"}, {ID: 100003, UUID: "u"}, {ID: 100004, UUID: "u"}}})
	for _, id := range n.userIDs {
		if !w.candidate(id, index) {
			t.Fatalf("user %d is not a candidate of node %d but was kept", id, index)
		}
	}
}
