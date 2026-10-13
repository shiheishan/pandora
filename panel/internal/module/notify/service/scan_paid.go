package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/repo"
	"github.com/google/uuid"
)

// paidCursor 是支付成功通知的游标名（notify_scan_cursors.name）。
const paidCursor = "order_paid"

// scanPaid 按 (履约时间, 订单 id) 游标翻一页（至多 scanBatch 张）已履约订单写支付成功通知，写完推进游标：
// 停机多久都不漏，高峰也不会被已通知过的单占住名额。中途有一件写失败时游标只推进到它前面，
// 下一轮从它重来（已写的靠去重键跳过）。还没有游标时从 paidLookback 之前开始。
//
// 套餐与到期时间取订单挂的订阅在扫描时的值：几分钟内连续续费或换套餐时显示的是最新一期，可以接受。
func (s *Service) scanPaid(ctx context.Context, now time.Time, loc *time.Location) (int, error) {
	cur, ok, err := s.store.ScanCursor(ctx, paidCursor)
	if err != nil {
		return 0, err
	}
	if !ok {
		cur = repo.ScanCursor{At: now.Add(-paidLookback)}
	}
	paid, err := s.orders.PaidForNotify(ctx, cur.At, cur.ID, scanBatch)
	if err != nil || len(paid) == 0 {
		return 0, err
	}
	ids := make([]uuid.UUID, len(paid))
	for i, p := range paid {
		ids[i] = p.SubscriptionID
	}
	subs, err := s.subs.SubscriptionsByIDs(ctx, ids)
	if err != nil {
		return 0, err
	}
	items := make([]scanItem, 0, len(paid))
	// owner[i] 是 items[i] 对应的 paid 下标；订阅已不存在的单没有条目，直接算处理完
	owner := make([]int, 0, len(paid))
	for i, p := range paid {
		sub, ok := subs[p.SubscriptionID]
		if !ok {
			continue
		}
		items = append(items, scanItem{
			UserID: p.UserID,
			Code:   TemplateOrderPaid,
			Vars: map[string]string{
				"order_no":   p.OrderNo,
				"plan":       sub.PlanID.String(),
				"expires_at": noticeTime(sub.PeriodEnd, loc),
			},
			Key: "order-paid:" + p.OrderID.String(),
		})
		owner = append(owner, i)
	}
	if err := s.resolvePlanNames(ctx, items); err != nil {
		return 0, err
	}
	written, done, werr := s.writeScanItems(ctx, items)
	last := len(paid) - 1
	if done < len(items) {
		last = owner[done] - 1
	}
	if last >= 0 {
		next := repo.ScanCursor{At: paid[last].FulfilledAt, ID: paid[last].OrderID}
		if err := s.store.SetScanCursor(ctx, paidCursor, next); err != nil {
			return written, err
		}
	}
	return written, werr
}
