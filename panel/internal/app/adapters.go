package app

import (
	"context"
	"errors"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	reportsvc "github.com/aegispanel/aegis/internal/module/report/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	supportsvc "github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"
)

// promoCatalog 把 catalog 的下单快照适配成 promo 要的 CatalogReader。
type promoCatalog struct{ c *catalogsvc.Service }

var _ promosvc.CatalogReader = promoCatalog{}

// MissingPlans 返回 ids 里不存在的套餐（已下架、已归档的也算存在）。
func (a promoCatalog) MissingPlans(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	found, err := a.c.PlansByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	missing := []uuid.UUID{}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// PriceSubtotal 按「新购」取在售价格：价格须属于 planID，按当前登录用户判断可见性与用户组；买不到一律回 NotFound。
func (a promoCatalog) PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (string, decimal.Decimal, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return "", decimal.Zero, asNotFound(err)
	}
	return string(o.Price.Currency), o.Price.UnitAmount, nil
}

// PackSubtotal 取在售流量包的币种与金额；下架或不存在回 NotFound。
func (a promoCatalog) PackSubtotal(ctx context.Context, packID uuid.UUID) (string, decimal.Decimal, error) {
	p, err := a.c.PackOffer(ctx, packID, false)
	if err != nil {
		return "", decimal.Zero, asNotFound(err)
	}
	return string(p.Currency), p.UnitAmount, nil
}

// asNotFound 把 catalog 的业务拒绝（不在售、未发布、下架等）统一成 NotFound，内部错误原样返回。
func asNotFound(err error) error {
	var e *errs.Error
	if errors.As(err, &e) && e.Code != errs.Internal {
		return errs.New(errs.NotFound)
	}
	return err
}

// opsOrderAdapter 把 order 的订单统计适配成 identity 要的 OpsOrders。
type opsOrderAdapter struct{ s *ordersvc.Service }

var _ identitysvc.OpsOrders = opsOrderAdapter{}

func (a opsOrderAdapter) UserOrderStats(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]identitysvc.OrderStat, error) {
	stats, err := a.s.UserOrderStats(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]identitysvc.OrderStat, len(stats))
	for id, st := range stats {
		out[id] = identitysvc.OrderStat{
			Orders: st.Orders,
			Paid:   st.Paid,
		}
	}
	return out, nil
}

// RecentOrders 包 order 的后台订单列表（按用户筛、取前 limit 单），给 identity 用户详情的最近订单。
func (a opsOrderAdapter) RecentOrders(ctx context.Context, userID uuid.UUID, limit int) ([]identitysvc.AdminOrderRow, error) {
	list, err := a.s.AdminOrders(ctx, ordersvc.ListFilter{UserID: &userID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]identitysvc.AdminOrderRow, 0, len(list.Orders))
	for _, v := range list.Orders {
		row := identitysvc.AdminOrderRow{
			ID:             v.ID,
			OrderNo:        v.OrderNo,
			UserEmail:      v.UserEmail,
			Kind:           v.Kind,
			Status:         v.Status,
			Currency:       string(v.Currency),
			Total:          v.Total,
			Payable:        v.Payable,
			Paid:           v.Paid,
			BalanceApplied: v.BalanceApplied,
			CreatedAt:      v.CreatedAt,
			PaidAt:         v.PaidAt,
			ProviderCode:   v.ProviderCode,
			ProviderName:   v.ProviderName,
			ItemCount:      v.ItemCount,
			Manual:         v.Manual,
		}
		if v.PlanName != nil {
			row.PlanName = *v.PlanName
		}
		if v.Interval != nil {
			row.Interval = *v.Interval
		}
		if v.IntervalCount != nil {
			row.IntervalCount = *v.IntervalCount
		}
		out = append(out, row)
	}
	return out, nil
}

// opsWalletAdapter 把 ledger 的站点币种与余额批量查询适配成 identity 后台用户列表/详情用的 OpsWallets。
type opsWalletAdapter struct{ l *ledgersvc.Service }

var _ identitysvc.OpsWallets = opsWalletAdapter{}

func (a opsWalletAdapter) UserBalances(ctx context.Context, userIDs []uuid.UUID) (string, map[uuid.UUID]decimal.Decimal, error) {
	cur, err := a.l.DefaultCurrency(ctx)
	if err != nil {
		return "", nil, err
	}
	bal, err := a.l.Balances(ctx, userIDs, cur)
	if err != nil {
		return "", nil, err
	}
	return string(cur), bal, nil
}

// groupUsageAdapter 组合 catalog、promo、node 的组引用统计，供 identity 后台用户组列表 OpsGroupUsage。
type groupUsageAdapter struct {
	catalog *catalogsvc.Service
	promo   *promosvc.Service
	node    *nodesvc.Service
}

var _ identitysvc.OpsGroupUsage = groupUsageAdapter{}

func (a groupUsageAdapter) GroupUsage(ctx context.Context) (map[uuid.UUID]identitysvc.GroupUsage, error) {
	refs, err := a.catalog.GroupRefCounts(ctx)
	if err != nil {
		return nil, err
	}
	coupons, err := a.promo.CouponCountsByGroup(ctx)
	if err != nil {
		return nil, err
	}
	pools, err := a.node.ExclusivePoolsByGroup(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]identitysvc.GroupUsage)
	for id, rc := range refs {
		u := out[id]
		u.Plans = rc.Plans
		u.Prices = rc.Prices
		out[id] = u
	}
	for id, n := range coupons {
		u := out[id]
		u.Coupons = n
		out[id] = u
	}
	for id, ps := range pools {
		u := out[id]
		for _, p := range ps {
			u.ExclusivePools = append(u.ExclusivePools, identitysvc.PoolRef{ID: p.ID, Name: p.Name})
		}
		out[id] = u
	}
	return out, nil
}

// supportOrders 让 support 经 order 校验门户用户关联的订单：MyOrder 按 ctx 里的门户主体限定本人，
// 不存在或不是本人的回 errs.NotFound。
type supportOrders struct{ o *ordersvc.Service }

// OwnedOrderNo 返回门户主体名下这张订单的订单号。
func (a supportOrders) OwnedOrderNo(ctx context.Context, orderID uuid.UUID) (string, error) {
	d, err := a.o.MyOrder(ctx, orderID)
	if err != nil {
		return "", err
	}
	return d.Order.OrderNo, nil
}

// supportPlans 让 support 经 subscription 批量取提单人当前生效订阅的套餐名（与订阅的「当前订阅」同一口径）。
type supportPlans struct{ s *subsvc.Service }

// ActivePlanNames 返回当前订阅生效的用户的套餐名；没有生效订阅的用户不在结果里。
func (a supportPlans) ActivePlanNames(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	sums, err := a.s.SummariesByUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(sums))
	for id, us := range sums {
		if us.Current != nil && us.Current.State == subsvc.StateLive {
			out[id] = us.Current.PlanName
		}
	}
	return out, nil
}

// supportReplyNotifier 让 support 经 notify 给提单人写「工单有新回复」；去重键按回复 id，同一条回复只通知一次。
type supportReplyNotifier struct{ n *notifysvc.Service }

// TicketReplied 在调用方事务里写通知。
func (a supportReplyNotifier) TicketReplied(ctx context.Context, n supportsvc.ReplyNotice) error {
	return a.n.NotifyUser(ctx, n.UserID, notifysvc.TemplateTicketReplied,
		map[string]string{"subject": n.Subject}, "ticket-replied:"+n.MessageID.String())
}

var (
	_ supportsvc.Orders        = supportOrders{}
	_ supportsvc.PlanNames     = supportPlans{}
	_ supportsvc.ReplyNotifier = supportReplyNotifier{}
)

// reportRedis 让 report 的系统状态页探测 Redis 是否可达。
type reportRedis struct{ c *redis.Client }

// Ping 做一次 PING 往返。
func (a reportRedis) Ping(ctx context.Context) error { return redisx.Ping(ctx, a.c) }

var _ reportsvc.Pinger = reportRedis{}
