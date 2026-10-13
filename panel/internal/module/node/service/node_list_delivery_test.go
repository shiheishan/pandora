package service

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/repo"
)

func nodeAllDeliveryFacts() nodeDeliveryFacts {
	return nodeDeliveryFacts{
		Serving: "active", Pooled: true, PoolActive: true, EverSeen: true,
		BeatFresh: true, Deliverable: true, ServerReady: true, Degraded: false,
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

func TestNodeDeliveryVerdict_neverSeen(t *testing.T) {
	f := nodeAllDeliveryFacts()
	f.EverSeen = false
	ok, note := nodeDeliveryVerdict(f)
	if ok || note != nodeNoteNeverSeen {
		t.Fatalf("got (%v, %q)", ok, note)
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
	st, af := nodeEffectiveState(r)
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
	_, af := nodeEffectiveState(r)
	if af == nil || af.Detail != reason {
		t.Fatalf("detail 为空时应取 reason，got Detail=%q", af.Detail)
	}
}

func TestNodeEffectiveState_applied(t *testing.T) {
	hash := "abc"
	r := repo.NodeListRow{AppliedConfigSHA256: &hash}
	st, af := nodeEffectiveState(r)
	if st != "applied" || af != nil {
		t.Fatalf("got st=%q af=%v", st, af)
	}
}

func TestNodeEffectiveState_pendingEmpty(t *testing.T) {
	r := repo.NodeListRow{}
	st, af := nodeEffectiveState(r)
	if st != "" || af != nil {
		t.Fatalf("无 applied 且无 failed 应为 (\"\", nil)，got (%q, %v)", st, af)
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
