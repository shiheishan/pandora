package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/google/uuid"
)

func nodeAllDeliveryFacts() nodeDeliveryFacts {
	return nodeDeliveryFacts{
		Bound: true, Serving: "active", Pooled: true, PoolActive: true, PoolBound: true, EverSeen: true,
		BeatFresh: true, Deliverable: true, ServerReady: true, Degraded: false,
	}
}

// 没绑服务器排在最前：即便状态、池这些也都不满足，也先给「未绑定服务器」。
func TestNodeDeliveryVerdict_unboundFirst(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Bound = false
	f.Serving = "draft"
	f.Pooled = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteUnbound {
		t.Fatalf("未绑服务器应最先判为 (false, unbound)，got (%v, %q)", ok, note)
	}
}

// 删服务器时节点先退役再清 server_id：这类节点不能再绑，说明照「未在役」给，不引导去绑定。
func TestNodeDeliveryVerdict_retiredUnboundIsNotActive(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Bound = false
	f.Serving = NodeRetired
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNotActive {
		t.Fatalf("退役且未绑应判为 (false, notActive)，got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_notActive(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Serving = "disabled"
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNotActive {
		t.Fatalf("got (%v, %q), want (false, notActive)", ok, note)
	}
}

func TestNodeDeliveryVerdict_noPool_beforeNeverSeen(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Pooled = false
	f.EverSeen = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNoPool {
		t.Fatalf("!Pooled 与 !EverSeen 同时成立时应先给 NoPool，got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_poolInactive(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.PoolActive = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNotePoolInactive {
		t.Fatalf("got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_poolUnbound(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.PoolBound = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNotePoolUnbound {
		t.Fatalf("got (%v, %q), want (false, poolUnbound)", ok, note)
	}
	if nodeNotePoolUnbound != "所在节点池没有绑定任何套餐，不会出现在任何用户的订阅里" {
		t.Fatalf("文案变了: %q", nodeNotePoolUnbound)
	}
}

func TestNodeDeliveryVerdict_poolUnbound_order(t *testing.T) {
	// 没划池、池未启用都排在「池没绑套餐」之前；「池没绑套餐」排在从未心跳之前
	f := nodeAllDeliveryFacts()
	f.PoolActive = false
	f.PoolBound = false
	if _, note := nodeDeliveryVerdict(f); note != nodeNotePoolInactive {
		t.Fatalf("池未启用应先于没绑套餐，got %q", note)
	}
	f = nodeAllDeliveryFacts()
	f.Pooled = false
	f.PoolActive = false
	f.PoolBound = false
	if _, note := nodeDeliveryVerdict(f); note != nodeNoteNoPool {
		t.Fatalf("没划池应最先，got %q", note)
	}
	f = nodeAllDeliveryFacts()
	f.PoolBound = false
	f.EverSeen = false
	if _, note := nodeDeliveryVerdict(f); note != nodeNotePoolUnbound {
		t.Fatalf("没绑套餐应先于从未心跳，got %q", note)
	}
}

func TestNodeDeliveryVerdict_neverSeen(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.EverSeen = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNeverSeen {
		t.Fatalf("got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_trafficExceeded(t *testing.T) {
	// 其余事实都满足、Deliverable: false、TrafficExceeded: true → (false, nodeNoteTrafficExceeded)
	f := nodeAllDeliveryFacts()
	f.Deliverable = false
	f.TrafficExceeded = true
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteTrafficExceeded {
		t.Fatalf("流量超限应为 (false, %q)，got (%v, %q)", nodeNoteTrafficExceeded, ok, note)
	}

	// ServerReady: false 同时超限也回超限文案
	f2 := nodeAllDeliveryFacts()
	f2.Deliverable = false
	f2.ServerReady = false
	f2.TrafficExceeded = true
	ok2, note2 := nodeDeliveryVerdict(f2)
	if ok2 || note2 != nodeNoteTrafficExceeded {
		t.Fatalf("ServerReady: false 且 TrafficExceeded 应先回超限文案，got (%v, %q)", ok2, note2)
	}
}

func TestNodeDeliveryVerdict_deliverableFalse_serverReady_nodeNotReady(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Deliverable = false
	f.ServerReady = true
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNodeNotReady {
		t.Fatalf("Deliverable=false 且 ServerReady=true 应给 NodeNotReady，got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_deliverableFalse_serverNotReady(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Deliverable = false
	f.ServerReady = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteServerNotReady {
		t.Fatalf("got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_staleStillDelivered(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.BeatFresh = false
	ok, note := nodeDeliveryVerdict(f)
	if !ok || note != nodeNoteStale {
		t.Fatalf("心跳不新鲜仍应下发，got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_degradedStillDelivered(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.Degraded = true
	ok, note := nodeDeliveryVerdict(f)
	if !ok || note != nodeNoteDegraded {
		t.Fatalf("全部满足但 Degraded 应 (true, Degraded)，got (%v, %q)", ok, note)
	}
}

func TestNodeDeliveryVerdict_allGood(t *testing.T) {
	f := nodeAllDeliveryFacts()
	ok, note := nodeDeliveryVerdict(f)
	if !ok || note != "" {
		t.Fatalf("全部满足应 (true, \"\")，got (%v, %q)", ok, note)
	}
}

func TestNodeEffectiveState_failedUsesDetailOverReason(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reason := "reason-only"
	detail := "detail-wins"
	r := repo.NodeListRow{
		ApplyFailedAt:     &at,
		ApplyFailedReason: &reason,
		ApplyFailedDetail: &detail,
	}
	st, af := nodeEffectiveState(r, nil)
	if st != "failed" || af == nil || af.Detail != detail || af.Phase != "failed" || !af.At.Equal(at) {
		t.Fatalf("failed 状态与 Detail 不符: st=%q af=%v", st, af)
	}
}

func TestNodeEffectiveState_failedEmptyDetailFallsBackToReason(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	reason := "from-reason"
	empty := ""
	r := repo.NodeListRow{
		ApplyFailedAt:     &at,
		ApplyFailedReason: &reason,
		ApplyFailedDetail: &empty,
	}
	_, af := nodeEffectiveState(r, nil)
	if af == nil || af.Detail != reason {
		t.Fatalf("detail 为空时应取 reason，got Detail=%q", af.Detail)
	}
}

func TestNodeEffectiveState_applied(t *testing.T) {
	hash := "abc"
	r := repo.NodeListRow{AppliedConfigSHA256: &hash}
	st, af := nodeEffectiveState(r, nil)
	if st != "applied" || af != nil {
		t.Fatalf("got st=%q af=%v", st, af)
	}
}

func TestNodeEffectiveState_pendingEmpty(t *testing.T) {
	r := repo.NodeListRow{}
	st, af := nodeEffectiveState(r, nil)
	if st != "" || af != nil {
		t.Fatalf("无 applied 且无 failed 应为 (\"\", nil)，got (%q, %v)", st, af)
	}
}

func TestNodeEffectiveState_desired(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	reason := "bind_failed"
	cur, old, other := "aaaa", "bbbb", "cccc"
	cases := []struct {
		name     string
		row      repo.NodeListRow
		desired  *string
		want     string
		wantFail bool
	}{
		{"已应用的正是期望", repo.NodeListRow{AppliedConfigSHA256: &cur}, &cur, "applied", false},
		{"已应用的是旧版", repo.NodeListRow{AppliedConfigSHA256: &old}, &cur, "pending", false},
		{"从没应用过", repo.NodeListRow{}, &cur, "pending", false},
		{"失败的正是期望", repo.NodeListRow{AppliedConfigSHA256: &old, ApplyFailedSHA256: &cur,
			ApplyFailedReason: &reason, ApplyFailedAt: &at}, &cur, "failed", true},
		{"失败的是别的版本、期望还没应用", repo.NodeListRow{AppliedConfigSHA256: &old, ApplyFailedSHA256: &other,
			ApplyFailedReason: &reason, ApplyFailedAt: &at}, &cur, "pending", true},
		{"不在该跑的集合里只看回报", repo.NodeListRow{AppliedConfigSHA256: &old}, nil, "applied", false},
	}
	for _, c := range cases {
		st, af := nodeEffectiveState(c.row, c.desired)
		if st != c.want || (af != nil) != c.wantFail {
			t.Fatalf("%s: got (%q, %v), want (%q, fail=%v)", c.name, st, af, c.want, c.wantFail)
		}
	}
}

func TestNodeDegraded_applyFailed(t *testing.T) {
	at := time.Now()
	r := repo.NodeListRow{ApplyFailedAt: &at}
	if !nodeDegraded(r) {
		t.Error("ApplyFailedAt 非空应为 degraded")
	}
}

func TestNodeDegraded_runtimeDownAndDegraded(t *testing.T) {
	down := "down"
	deg := "degraded"
	if !nodeDegraded(repo.NodeListRow{RuntimeStatus: &down}) {
		t.Error("runtime down 应为 degraded")
	}
	if !nodeDegraded(repo.NodeListRow{RuntimeStatus: &deg}) {
		t.Error("runtime degraded 应为 true")
	}
}

func TestNodeDegraded_runtimeRunningFalse(t *testing.T) {
	run := "running"
	if nodeDegraded(repo.NodeListRow{RuntimeStatus: &run}) {
		t.Error("running 不应算 degraded")
	}
	if nodeDegraded(repo.NodeListRow{RuntimeStatus: nil}) {
		t.Error("无 runtime 不应算 degraded")
	}
}

// 面板生成配置文档失败的节点：不管节点回报过什么，列表都显示为失败，详情写明是面板这边的错。
func TestNodeRowFromList_configSkipFailed(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	bad, good := uuid.New(), uuid.New()
	applied := "abcd"
	x := nodeListExtras{
		desired:     map[uuid.UUID]string{good: applied},
		configSkips: map[uuid.UUID]configSkip{bad: {ServerID: uuid.New(), Reason: "协议配置不是 JSON 对象"}},
	}

	nr := nodeRowFromList(repo.NodeListRow{ID: bad, AppliedConfigSHA256: &applied}, now, false, x)
	if nr.EffectiveState != "failed" || nr.LastApplyFailure == nil {
		t.Fatalf("跳过的节点应为 failed，got %q %v", nr.EffectiveState, nr.LastApplyFailure)
	}
	if f := nr.LastApplyFailure; f.Phase != "failed" || f.Detail != "面板生成配置失败：协议配置不是 JSON 对象" || !f.At.Equal(now) {
		t.Fatalf("失败详情不符: %+v", f)
	}

	nr = nodeRowFromList(repo.NodeListRow{ID: good, AppliedConfigSHA256: &applied}, now, false, x)
	if nr.EffectiveState != "applied" || nr.LastApplyFailure != nil {
		t.Fatalf("没被跳过的节点照旧按期望摘要算，got %q %v", nr.EffectiveState, nr.LastApplyFailure)
	}
}
