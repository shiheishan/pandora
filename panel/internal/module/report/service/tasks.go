package service

import (
	"context"
	"time"

	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
)

// backlogFrom 把 notify 的投递积压读数转成看板用的 NotificationBacklog（含积压状态与 MaxReadyLag）。
func backlogFrom(b notifysvc.DeliveryBacklog, now time.Time) NotificationBacklog {
	out := NotificationBacklog{
		AsOf:           now,
		Ready:          b.Ready,
		ReadyRetry:     b.ReadyRetry,
		Scheduled:      b.Scheduled,
		ScheduledRetry: b.ScheduledRetry,
		Failed:         b.Failed,
		Suppressed:     b.Suppressed,
		OldestReadyAt:  b.OldestReadyAt,
		LastSentAt:     b.LastSentAt,
	}
	thresholdSec := int64(BacklogThreshold / time.Second)
	if b.Ready == 0 {
		out.MaxReadyLag = 0
		out.State = "clear"
		out.Reason = "no_due_backlog"
		return out
	}
	if b.OldestReadyAt != nil {
		lag := int64(now.Sub(*b.OldestReadyAt) / time.Second)
		if lag < 0 {
			lag = 0
		}
		out.MaxReadyLag = lag
	} else {
		out.MaxReadyLag = 0
	}
	if out.MaxReadyLag > thresholdSec {
		out.State = "backlogged"
		out.Reason = "lag_exceeded"
	} else {
		out.State = "clear"
		out.Reason = "within_threshold"
	}
	return out
}

// NotificationBacklog 给通知投递积压：到期未发的最早一条等了多久，超过 BacklogThreshold 即 backlogged。
func (s *Service) NotificationBacklog(ctx context.Context) (NotificationBacklog, error) {
	now := s.now()
	b, err := s.deps.Notices.DeliveryBacklog(ctx, now)
	if err != nil {
		return NotificationBacklog{}, err
	}
	return backlogFrom(b, now), nil
}

// Tasks 给后台「需要处理」的六项：待处理工单、待审核提现、失联节点、超时未支付订单、通知积压、账本漂移。后台不分权限，六项都给。
func (s *Service) Tasks(ctx context.Context) (Tasks, error) {
	now := s.now()

	tickets, err := s.deps.Tickets.OpenTicketStats(ctx, now)
	if err != nil {
		return Tasks{}, err
	}

	withdrawals, err := s.deps.Commissions.PendingWithdrawalSums(ctx)
	if err != nil {
		return Tasks{}, err
	}
	var wCount int64
	wAmounts := make([]CurrencyAmount, 0, len(withdrawals))
	for _, w := range withdrawals {
		wCount += w.Count
		wAmounts = append(wAmounts, CurrencyAmount{Currency: w.Currency, Amount: w.Amount})
	}

	offline, err := s.deps.Nodes.OfflineNodes(ctx, now, 3)
	if err != nil {
		return Tasks{}, err
	}
	sample := make([]NodeRef, len(offline.Sample))
	for i, n := range offline.Sample {
		sample[i] = NodeRef{ID: n.ID, Name: n.Name}
	}

	staleOrders, err := s.deps.Orders.PendingStaleCount(ctx, now.Add(-StaleOrderAfter))
	if err != nil {
		return Tasks{}, err
	}

	b, err := s.deps.Notices.DeliveryBacklog(ctx, now)
	if err != nil {
		return Tasks{}, err
	}
	bl := backlogFrom(b, now)

	drift, err := s.deps.Ledger.DriftAccounts(ctx)
	if err != nil {
		return Tasks{}, err
	}

	return Tasks{
		AsOf: now,
		TicketsOpen: TicketsOpenTask{
			Count:             tickets.Count,
			HighPriority:      tickets.HighPriority,
			OldestWaitSeconds: tickets.OldestWaitSeconds,
		},
		WithdrawalsPending: WithdrawalsPendingTask{
			Count:   wCount,
			Amounts: wAmounts,
		},
		NodesOffline: NodesOfflineTask{
			Count:                 offline.Count,
			Sample:                sample,
			LongestOfflineSeconds: offline.LongestOfflineSeconds,
		},
		OrdersPendingStale: OrdersPendingStaleTask{
			Count:            staleOrders,
			ThresholdSeconds: int64(StaleOrderAfter / time.Second),
		},
		NotificationsBacklog: NotificationsBacklogTask{
			Queued:       bl.Ready + bl.Scheduled,
			FailedTotal:  bl.Failed,
			BacklogState: bl.State,
		},
		LedgerDrift: drift,
	}, nil
}
