package service

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 列表分页口径。
const (
	listLimitDefault = 20
	listLimitMax     = 100
	// adminEmailMatchMax 是后台按邮箱搜订单时最多取的用户数。
	adminEmailMatchMax = 1000
	// topupItemName 是充值单（没有订单行）在列表上的商品名。
	topupItemName = "余额充值"
)

// toRow 把订单行转成对外的订单视图。
func toRow(o model.Order) OrderRow {
	return OrderRow{
		ID:                o.ID,
		OrderNo:           o.OrderNo,
		UserID:            o.UserID,
		Kind:              o.Kind,
		Status:            o.Status,
		Currency:          money.Currency(o.Currency),
		Subtotal:          o.SubtotalAmount,
		Discount:          o.DiscountAmount,
		ProrationCredit:   o.ProrationCreditAmount,
		Waived:            o.WaivedAmount,
		Total:             o.TotalAmount,
		BalanceApplied:    o.BalanceApplied,
		Payable:           o.PayableAmount,
		Paid:              o.PaidAmount,
		CouponID:          o.CouponID,
		CreatedBy:         o.CreatedBy,
		ManualReason:      o.ManualReason,
		SubscriptionID:    o.SubscriptionID,
		SubscriptionLabel: o.SubscriptionLabel,
		StateVersion:      o.StateVersion,
		ExpiresAt:         o.ExpiresAt,
		PaidAt:            o.PaidAt,
		FulfilledAt:       o.FulfilledAt,
		CancelledAt:       o.CancelledAt,
		ExpiredAt:         o.ExpiredAt,
		CancelReason:      o.CancelReason,
		CreatedAt:         o.CreatedAt,
		UpdatedAt:         o.UpdatedAt,
	}
}

// toItem 把订单行快照转成对外视图。
func toItem(it model.Item) ItemRow {
	return ItemRow{
		ID:            it.ID,
		PriceID:       it.PriceID,
		PlanID:        it.PlanID,
		PlanVersionID: it.PlanVersionID,
		TrafficPackID: it.TrafficPackID,
		Name:          it.SnapshotName,
		PlanVersion:   it.SnapshotPlanVersion,
		Interval:      it.SnapshotInterval,
		IntervalCount: it.SnapshotIntervalCount,
		TrafficBytes:  it.SnapshotTrafficBytes,
		Entitlements:  it.SnapshotEntitlements,
		Quotas:        it.SnapshotQuotas,
		Quantity:      it.Quantity,
		UnitAmount:    it.UnitAmount,
		LineAmount:    it.LineAmount,
		Currency:      money.Currency(it.Currency),
		CreatedAt:     it.CreatedAt,
	}
}

// normalizeStatuses 校验并去重状态筛选；不认识的值回 ErrUnknownStatus。
func normalizeStatuses(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, st := range in {
		st = strings.TrimSpace(st)
		if st == "" || seen[st] {
			continue
		}
		known := false
		for _, f := range FilterStatuses {
			if f == st {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrUnknownStatus
		}
		seen[st] = true
		out = append(out, st)
	}
	return out, nil
}

// page 规范化分页参数。
func page(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = listLimitDefault
	}
	if limit > listLimitMax {
		limit = listLimitMax
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// enriched 是一批订单批量补全后的附加信息（每种一条查询，不逐行查）。
type enriched struct {
	items     map[uuid.UUID][]model.Item
	summaries map[uuid.UUID]paymentsvc.OrderPaymentSummary
	emails    map[uuid.UUID]string // 只有后台补
}

// enrich 批量取一批订单的订单行与支付摘要；withEmails 为真时再取下单用户（与人工单开单人）的邮箱。
func (s *Service) enrich(ctx context.Context, orders []model.Order, withEmails bool) (enriched, error) {
	out := enriched{
		items:     map[uuid.UUID][]model.Item{},
		summaries: map[uuid.UUID]paymentsvc.OrderPaymentSummary{},
		emails:    map[uuid.UUID]string{},
	}
	if len(orders) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, 0, len(orders))
	for _, o := range orders {
		ids = append(ids, o.ID)
	}
	items, err := s.repo.ItemsByOrders(ctx, ids)
	if err != nil {
		return enriched{}, err
	}
	for _, it := range items {
		out.items[it.OrderID] = append(out.items[it.OrderID], it)
	}
	sums, err := s.payments.OrderSummaries(ctx, ids)
	if err != nil {
		return enriched{}, err
	}
	if sums != nil {
		out.summaries = sums
	}
	if withEmails {
		seen := map[uuid.UUID]bool{}
		users := make([]uuid.UUID, 0, len(orders))
		for _, o := range orders {
			for _, u := range []*uuid.UUID{&o.UserID, o.CreatedBy} {
				if u != nil && !seen[*u] {
					seen[*u] = true
					users = append(users, *u)
				}
			}
		}
		emails, err := s.users.Emails(ctx, users)
		if err != nil {
			return enriched{}, err
		}
		if emails != nil {
			out.emails = emails
		}
	}
	return out, nil
}

// myView 组门户订单视图：首条订单行给商品名、周期与 plan_name；充值单商品名固定。
func (e enriched) myView(o model.Order) MyOrderView {
	v := MyOrderView{
		OrderRow:         toRow(o),
		Cancellable:      o.Status == StatusPending,
		HasPaymentIntent: e.summaries[o.ID].HasIntent,
	}
	if items := e.items[o.ID]; len(items) > 0 {
		first := items[0]
		v.ItemName = first.SnapshotName
		v.Interval = first.SnapshotInterval
		v.IntervalCount = first.SnapshotIntervalCount
		// 旧接口的 plan_name 是首项快照名（流量包订单即流量包名）
		name := first.SnapshotName
		v.PlanName = &name
	}
	if v.ItemName == "" && o.Kind == KindTopup {
		v.ItemName = topupItemName
	}
	return v
}

// adminView 组后台订单视图。
func (e enriched) adminView(o model.Order) AdminOrderView {
	sum := e.summaries[o.ID]
	v := AdminOrderView{
		OrderRow:     toRow(o),
		UserEmail:    e.emails[o.UserID],
		Manual:       o.CreatedBy != nil,
		ProviderCode: sum.ProviderCode,
		ProviderName: sum.ProviderName,
	}
	items := e.items[o.ID]
	v.ItemCount = len(items)
	if len(items) > 0 {
		first := items[0]
		v.Interval = first.SnapshotInterval
		v.IntervalCount = first.SnapshotIntervalCount
		if first.PlanID != nil {
			name := first.SnapshotName
			v.PlanName = &name
		}
	}
	return v
}

// MyOrders 门户「我的订单」：本人订单分页，带分组计数（不受筛选影响）。
func (s *Service) MyOrders(ctx context.Context, f ListFilter) (MyOrderList, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return MyOrderList{}, err
	}
	statuses, err := normalizeStatuses(f.Statuses)
	if err != nil {
		return MyOrderList{}, err
	}
	limit, offset := page(f.Limit, f.Offset)
	orders, total, err := s.repo.ListOrders(ctx, repo.ListParams{
		UserID:   &userID,
		Statuses: statuses,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return MyOrderList{}, err
	}
	counts, err := s.repo.StatusCountsForUser(ctx, userID)
	if err != nil {
		return MyOrderList{}, err
	}
	e, err := s.enrich(ctx, orders, false)
	if err != nil {
		return MyOrderList{}, err
	}
	out := MyOrderList{
		Orders: make([]MyOrderView, 0, len(orders)),
		Total:  total,
		Counts: MyOrderCounts{
			Open:   counts[StatusPending],
			Paid:   counts[StatusPaid] + counts[StatusFulfilled],
			Closed: counts[StatusCancelled] + counts[StatusExpired],
		},
	}
	for _, o := range orders {
		out.Orders = append(out.Orders, e.myView(o))
	}
	return out, nil
}

// MyOrder 门户订单详情：订单行、收款、优惠码与订阅到期时间。
func (s *Service) MyOrder(ctx context.Context, id uuid.UUID) (MyOrderDetail, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return MyOrderDetail{}, err
	}
	o, ok, err := s.repo.OrderForUser(ctx, id, userID, repo.NoLock)
	if err != nil {
		return MyOrderDetail{}, err
	}
	if !ok {
		return MyOrderDetail{}, ErrNotFound
	}
	e, err := s.enrich(ctx, []model.Order{o}, false)
	if err != nil {
		return MyOrderDetail{}, err
	}
	out := MyOrderDetail{
		Order:    e.myView(o),
		Items:    make([]ItemRow, 0, len(e.items[o.ID])),
		Payments: []PaymentLine{},
	}
	for _, it := range e.items[o.ID] {
		out.Items = append(out.Items, toItem(it))
	}
	hist, err := s.payments.OrderHistory(ctx, o.ID)
	if err != nil {
		return MyOrderDetail{}, err
	}
	for _, p := range hist.Payments {
		out.Payments = append(out.Payments, PaymentLine{
			Status:       p.Status,
			Amount:       p.Amount,
			Currency:     p.Currency,
			CreatedAt:    p.PaidAt,
			Method:       p.Method,
			ProviderName: p.ProviderName,
		})
	}
	if o.CouponID != nil {
		codes, err := s.promo.Codes(ctx, []uuid.UUID{*o.CouponID})
		if err != nil {
			return MyOrderDetail{}, err
		}
		if code, ok := codes[*o.CouponID]; ok {
			out.CouponCode = &code
		}
	}
	if o.SubscriptionID != nil {
		sub, err := s.subs.Get(ctx, *o.SubscriptionID, &userID)
		switch {
		case err == nil:
			end := sub.PeriodEnd
			out.SubscriptionPeriodEnd = &end
		case errors.Is(err, subsvc.ErrNotFound):
			// 订阅找不到就不显示到期时间
		default:
			return MyOrderDetail{}, err
		}
	}
	return out, nil
}

// AdminOrders 后台订单列表：Q 按订单号（包含）或用户邮箱（包含）搜。
func (s *Service) AdminOrders(ctx context.Context, f ListFilter) (AdminOrderList, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return AdminOrderList{}, err
	}
	statuses, err := normalizeStatuses(f.Statuses)
	if err != nil {
		return AdminOrderList{}, err
	}
	q := strings.TrimSpace(f.Q)
	if err := rejectNUL("q", q); err != nil {
		return AdminOrderList{}, err
	}
	limit, offset := page(f.Limit, f.Offset)
	p := repo.ListParams{
		UserID:   f.UserID,
		Statuses: statuses,
		From:     f.From,
		To:       f.To,
		Limit:    limit,
		Offset:   offset,
	}
	if q != "" {
		ids, err := s.users.IDsByEmail(ctx, q, adminEmailMatchMax)
		if err != nil {
			return AdminOrderList{}, err
		}
		p.OrderNoLike = q
		p.UserIDs = ids
	}
	orders, total, err := s.repo.ListOrders(ctx, p)
	if err != nil {
		return AdminOrderList{}, err
	}
	e, err := s.enrich(ctx, orders, true)
	if err != nil {
		return AdminOrderList{}, err
	}
	out := AdminOrderList{Orders: make([]AdminOrderView, 0, len(orders)), Total: total}
	for _, o := range orders {
		out.Orders = append(out.Orders, e.adminView(o))
	}
	return out, nil
}

// AdminOrder 后台订单详情。
func (s *Service) AdminOrder(ctx context.Context, id uuid.UUID) (AdminOrderDetail, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return AdminOrderDetail{}, err
	}
	o, ok, err := s.repo.OrderByID(ctx, id, repo.NoLock)
	if err != nil {
		return AdminOrderDetail{}, err
	}
	if !ok {
		return AdminOrderDetail{}, ErrNotFound
	}
	e, err := s.enrich(ctx, []model.Order{o}, true)
	if err != nil {
		return AdminOrderDetail{}, err
	}
	out := AdminOrderDetail{
		Order: e.adminView(o),
		Items: make([]ItemRow, 0, len(e.items[o.ID])),
	}
	if o.CreatedBy != nil {
		if email, ok := e.emails[*o.CreatedBy]; ok {
			out.CreatedByEmail = &email
		}
	}
	for _, it := range e.items[o.ID] {
		out.Items = append(out.Items, toItem(it))
	}
	return out, nil
}

// AdminOrderPayments 后台订单的支付尝试与收款。
func (s *Service) AdminOrderPayments(ctx context.Context, id uuid.UUID) (paymentsvc.OrderHistory, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return paymentsvc.OrderHistory{}, err
	}
	_, ok, err := s.repo.OrderByID(ctx, id, repo.NoLock)
	if err != nil {
		return paymentsvc.OrderHistory{}, err
	}
	if !ok {
		return paymentsvc.OrderHistory{}, ErrNotFound
	}
	return s.payments.OrderHistory(ctx, id)
}
