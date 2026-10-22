package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
)

func TestBacklogStates(t *testing.T) {
	now := testNow
	fiveMinAgo := now.Add(-5 * time.Minute)
	elevenMinAgo := now.Add(-11 * time.Minute)
	oneMinFuture := now.Add(time.Minute)

	cases := []struct {
		name   string
		b      notifysvc.DeliveryBacklog
		state  string
		reason string
		lag    int64
	}{
		{
			name:   "ready_zero",
			b:      notifysvc.DeliveryBacklog{Ready: 0},
			state:  "clear",
			reason: "no_due_backlog",
			lag:    0,
		},
		{
			name: "within_threshold",
			b: notifysvc.DeliveryBacklog{
				Ready:         2,
				OldestReadyAt: &fiveMinAgo,
			},
			state:  "clear",
			reason: "within_threshold",
			lag:    300,
		},
		{
			name: "lag_exceeded",
			b: notifysvc.DeliveryBacklog{
				Ready:         1,
				OldestReadyAt: &elevenMinAgo,
			},
			state:  "backlogged",
			reason: "lag_exceeded",
			lag:    660,
		},
		{
			name: "clock_skew_future_oldest",
			b: notifysvc.DeliveryBacklog{
				Ready:         1,
				OldestReadyAt: &oneMinFuture,
			},
			state:  "clear",
			reason: "within_threshold",
			lag:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := backlogFrom(tc.b, now)
			if out.State != tc.state || out.Reason != tc.reason || out.MaxReadyLag != tc.lag {
				t.Fatalf("State=%q Reason=%q MaxReadyLag=%d; want %q %q %d",
					out.State, out.Reason, out.MaxReadyLag, tc.state, tc.reason, tc.lag)
			}
			if out.AsOf != now {
				t.Fatalf("AsOf=%v want %v", out.AsOf, now)
			}
		})
	}
}

func TestNotificationBacklogUsesNow(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()
	old := testNow.Add(-2 * time.Minute)
	sent := testNow.Add(-time.Hour)
	e.ports.backlog = notifysvc.DeliveryBacklog{
		Ready:          4,
		ReadyRetry:     1,
		Scheduled:      6,
		ScheduledRetry: 2,
		Failed:         9,
		Suppressed:     3,
		OldestReadyAt:  &old,
		LastSentAt:     &sent,
	}

	out, err := e.svc.NotificationBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out.AsOf != testNow {
		t.Fatalf("AsOf=%v want %v", out.AsOf, testNow)
	}
	if out.Ready != 4 || out.ReadyRetry != 1 || out.Scheduled != 6 || out.ScheduledRetry != 2 ||
		out.Failed != 9 || out.Suppressed != 3 {
		t.Fatalf("counts mismatch: %+v", out)
	}
	if out.OldestReadyAt == nil || !out.OldestReadyAt.Equal(old) {
		t.Fatalf("OldestReadyAt=%v want %v", out.OldestReadyAt, old)
	}
	if out.LastSentAt == nil || !out.LastSentAt.Equal(sent) {
		t.Fatalf("LastSentAt=%v want %v", out.LastSentAt, sent)
	}
}

func TestTasksMapping(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	wait120 := int64(120)
	e.ports.tickets = supportsvc.OpenTicketStats{Count: 3, HighPriority: 1, OldestWaitSeconds: &wait120}
	e.ports.withdrawals = commissionsvc.PendingWithdrawalSum{
		Count: 3, Amount: dec("35.00"),
	}
	id1, id2 := uuid.MustParse("00000000-0000-4000-8000-000000000001"), uuid.MustParse("00000000-0000-4000-8000-000000000002")
	long900 := int64(900)
	e.ports.offline = nodesvc.OfflineNodes{
		Count: 4,
		Sample: []nodesvc.NodeRef{
			{ID: id1, Name: "node-a"},
			{ID: id2, Name: "node-b"},
		},
		LongestOfflineSeconds: &long900,
	}
	e.ports.certs = nodesvc.CertAttention{
		Count: 5, Failing: 2, Expiring: 3,
		Sample: []nodesvc.NodeRef{{ID: id2, Name: "cert-b"}},
	}
	e.ports.pendingStale = 6
	oneMinAgo := testNow.Add(-time.Minute)
	e.ports.backlog = notifysvc.DeliveryBacklog{
		Ready:         2,
		Scheduled:     3,
		Failed:        7,
		OldestReadyAt: &oneMinAgo,
	}
	// 待办里的巡检读表：只给登记着的检查、按检查顺序；不认识的旧代号不给
	e.store.integrity[CheckOrderPaidUnfulfilled] = model.IntegrityCheck{Code: CheckOrderPaidUnfulfilled, Count: 0, Sample: "[]", CheckedAt: testNow}
	e.store.integrity[CheckLedgerDrift] = model.IntegrityCheck{Code: CheckLedgerDrift, Count: 1, Sample: `["x"]`, CheckedAt: testNow}
	e.store.integrity["retired_check"] = model.IntegrityCheck{Code: "retired_check", Count: 9, Sample: "[]", CheckedAt: testNow}

	tasks, err := e.svc.Tasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tasks.AsOf != testNow {
		t.Fatalf("AsOf=%v want %v", tasks.AsOf, testNow)
	}
	if tasks.TicketsOpen.Count != 3 || tasks.TicketsOpen.HighPriority != 1 ||
		tasks.TicketsOpen.OldestWaitSeconds == nil || *tasks.TicketsOpen.OldestWaitSeconds != 120 {
		t.Fatalf("TicketsOpen=%+v", tasks.TicketsOpen)
	}
	if tasks.WithdrawalsPending.Count != 3 || !tasks.WithdrawalsPending.Amount.Equal(dec("35.00")) {
		t.Fatalf("WithdrawalsPending=%+v", tasks.WithdrawalsPending)
	}
	if tasks.NodesOffline.Count != 4 || len(tasks.NodesOffline.Sample) != 2 {
		t.Fatalf("NodesOffline=%+v", tasks.NodesOffline)
	}
	if tasks.NodesOffline.Sample[0].ID != id1 || tasks.NodesOffline.Sample[0].Name != "node-a" ||
		tasks.NodesOffline.Sample[1].ID != id2 || tasks.NodesOffline.Sample[1].Name != "node-b" {
		t.Fatalf("sample=%+v", tasks.NodesOffline.Sample)
	}
	if tasks.NodeCerts.Count != 5 || tasks.NodeCerts.Failing != 2 || tasks.NodeCerts.Expiring != 3 {
		t.Fatalf("NodeCerts=%+v", tasks.NodeCerts)
	}
	if len(tasks.NodeCerts.Sample) != 1 || tasks.NodeCerts.Sample[0].ID != id2 || tasks.NodeCerts.Sample[0].Name != "cert-b" {
		t.Fatalf("NodeCerts.Sample=%+v", tasks.NodeCerts.Sample)
	}
	if tasks.OrdersPendingStale.Count != 6 || tasks.OrdersPendingStale.ThresholdSeconds != 1800 {
		t.Fatalf("OrdersPendingStale=%+v", tasks.OrdersPendingStale)
	}
	if tasks.NotificationsBacklog.Queued != 5 || tasks.NotificationsBacklog.FailedTotal != 7 ||
		tasks.NotificationsBacklog.BacklogState != "clear" {
		t.Fatalf("NotificationsBacklog=%+v", tasks.NotificationsBacklog)
	}
	want := []IntegrityItem{
		{Code: CheckLedgerDrift, Count: 1, CheckedAt: testNow},
		{Code: CheckOrderPaidUnfulfilled, Count: 0, CheckedAt: testNow},
	}
	if !slices.Equal(tasks.Integrity, want) {
		t.Fatalf("Integrity=%+v want %+v", tasks.Integrity, want)
	}
}

func TestTasksEmptySlices(t *testing.T) {
	e := newTestEnv()
	tasks, err := e.svc.Tasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tasks.NodesOffline.Sample == nil || len(tasks.NodesOffline.Sample) != 0 {
		t.Fatalf("NodesOffline.Sample=%v", tasks.NodesOffline.Sample)
	}
	if tasks.NodeCerts.Sample == nil || len(tasks.NodeCerts.Sample) != 0 {
		t.Fatalf("NodeCerts.Sample=%v", tasks.NodeCerts.Sample)
	}
}

func TestTasksPortError(t *testing.T) {
	e := newTestEnv()
	e.ports.errFor["PendingWithdrawalSums"] = errBoom
	_, err := e.svc.Tasks(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("err=%v want errBoom", err)
	}

	e2 := newTestEnv()
	e2.ports.errFor["PendingStaleCount"] = errBoom
	_, err = e2.svc.Tasks(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("err=%v want errBoom", err)
	}

	e3 := newTestEnv()
	e3.ports.errFor["CertAttentionNodes"] = errBoom
	_, err = e3.svc.Tasks(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("err=%v want errBoom", err)
	}
}
