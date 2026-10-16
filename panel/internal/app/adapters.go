package app

import (
	"context"
	"errors"

	authsvc "github.com/aegispanel/aegis/internal/module/auth/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	commissionsvc "github.com/aegispanel/aegis/internal/module/commission/service"
	contentsvc "github.com/aegispanel/aegis/internal/module/content/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	reportsvc "github.com/aegispanel/aegis/internal/module/report/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
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

// PriceSubtotal 按「新购」取在售价格的金额与计费周期（billing_interval），一次 catalog 查询；价格须属于 planID，按当前登录用户判断可见性与用户组；买不到一律回 NotFound（xb-order #34：券限定计费周期的试算用）。
func (a promoCatalog) PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (decimal.Decimal, string, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return decimal.Zero, "", asNotFound(err)
	}
	return o.Price.UnitAmount, o.Price.BillingInterval, nil
}

// PackSubtotal 取在售流量包的金额；下架或不存在回 NotFound。
func (a promoCatalog) PackSubtotal(ctx context.Context, packID uuid.UUID) (decimal.Decimal, error) {
	p, err := a.c.PackOffer(ctx, packID, false)
	if err != nil {
		return decimal.Zero, asNotFound(err)
	}
	return p.UnitAmount, nil
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

// opsWalletAdapter 把 ledger 的余额批量查询适配成 identity 后台用户列表/详情用的 OpsWallets。
type opsWalletAdapter struct{ l *ledgersvc.Service }

var _ identitysvc.OpsWallets = opsWalletAdapter{}

func (a opsWalletAdapter) UserBalances(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	return a.l.Balances(ctx, userIDs)
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

// commissionVoidAdapter 把 commission 的注销作废适配成 identity 要的 CommissionVoider。
type commissionVoidAdapter struct{ s *commissionsvc.Service }

var _ identitysvc.CommissionVoider = commissionVoidAdapter{}

func (a commissionVoidAdapter) LockForDeletion(ctx context.Context, userID uuid.UUID) error {
	return a.s.LockForDeletion(ctx, userID)
}

func (a commissionVoidAdapter) VoidForDeletedUser(ctx context.Context, userID uuid.UUID) (identitysvc.CommissionVoid, error) {
	r, err := a.s.VoidForDeletedUser(ctx, userID)
	if err != nil {
		return identitysvc.CommissionVoid{}, err
	}
	return identitysvc.CommissionVoid{
		VoidedEntries:       r.VoidedEntries,
		VoidedPending:       r.VoidedPending,
		Forfeited:           r.Forfeited,
		RejectedWithdrawals: r.RejectedWithdrawals,
		RejectedAmount:      r.RejectedAmount,
	}, nil
}

// ---- xb-tg：Telegram 用户命令、管理员群组提醒与在 TG 里回复工单、知识库占位变量 ----

// notifySubscribeLinks 让 notify 的 /sub 经 subscription 取用户的订阅链接：与门户「订阅链接」同一口径（MyLinks），
// 只要生效中的。MyLinks 按 ctx 里的门户主体取，这里以该用户的门户主体调用（webhook 没有登录会话）。
type notifySubscribeLinks struct{ s *subsvc.Service }

// ActiveLinks 返回该用户每份生效订阅当前的订阅链接（按凭据创建时间倒序），没有返回空切片。
func (a notifySubscribeLinks) ActiveLinks(ctx context.Context, userID uuid.UUID) ([]string, error) {
	ctx = authn.With(ctx, authn.Principal{UserID: userID, SessionID: "telegram", Audience: authn.AudiencePublic})
	links, err := a.s.MyLinks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		if !l.Expired {
			out = append(out, l.URL)
		}
	}
	return out, nil
}

// notifyBalances 让 notify 的工单提醒经 ledger 批量取用户余额。
type notifyBalances struct{ l *ledgersvc.Service }

// UserBalances 返回余额；没有账户的用户为 0。
func (a notifyBalances) UserBalances(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	return a.l.Balances(ctx, userIDs)
}

// notifyTicketReplier 让 notify 在 Telegram 管理员群组里收到的回复以该管理员身份回复工单：与后台回复走同一个
// support.AgentReply（同样的状态流转、通知提单人、审计，审计 data 带 via=telegram）。
// 后台只读模式（admin.writes 关闭）时与后台接口一样拒绝。
type notifyTicketReplier struct {
	support  *supportsvc.Service
	switches interface {
		SwitchEnabled(ctx context.Context, code string) (bool, error)
	}
}

// ReplyAsAdmin 以 adminID 的后台主体公开回复工单。
func (a notifyTicketReplier) ReplyAsAdmin(ctx context.Context, adminID, ticketID uuid.UUID, body string) error {
	on, err := a.switches.SwitchEnabled(ctx, settingssvc.SwitchAdminWrites)
	if err != nil {
		return err
	}
	if !on {
		return errs.Newf(errs.Unavailable, "管理端只读模式")
	}
	ctx = authn.With(ctx, authn.Principal{UserID: adminID, SessionID: "telegram", Audience: authn.AudienceAdmin})
	return a.support.AgentReply(ctx, supportsvc.AgentReplyInput{TicketID: ticketID, Body: body, Source: supportsvc.SourceTelegram})
}

// supportAdminNotifier 让 support 在建单、用户回复的同一事务里经 notify 往管理员群组排一条工单提醒
// （后台开关没开、群组没配时 notify 什么都不写）。
type supportAdminNotifier struct{ n *notifysvc.Service }

// TicketActivity 在调用方事务里写提醒。
func (a supportAdminNotifier) TicketActivity(ctx context.Context, n supportsvc.AdminTicketNotice) error {
	return a.n.NotifyAdminsTicket(ctx, notifysvc.AdminTicketInput{
		Kind: n.Kind, UserID: n.UserID, TicketID: n.TicketID, MessageID: n.MessageID,
		TicketNo: n.TicketNo, Subject: n.Subject, Body: n.Body,
	})
}

// contentSubscribeLinks 让 content 的知识库占位变量 {{subscribeUrl}} 经 subscription 取门户主体的订阅链接：
// 多份生效订阅时取凭据最新的那份（MyLinks 已按凭据创建时间倒序）。
type contentSubscribeLinks struct{ s *subsvc.Service }

// CurrentLink 返回 ctx 里门户主体最新一份生效订阅的订阅链接；没有生效订阅返回空串。
func (a contentSubscribeLinks) CurrentLink(ctx context.Context) (string, error) {
	links, err := a.s.MyLinks(ctx)
	if err != nil {
		return "", err
	}
	for _, l := range links {
		if !l.Expired {
			return l.URL, nil
		}
	}
	return "", nil
}

var (
	_ notifysvc.SubscribeLinks        = notifySubscribeLinks{}
	_ notifysvc.Balances              = notifyBalances{}
	_ notifysvc.TicketReplier         = notifyTicketReplier{}
	_ notifysvc.SubscriptionSummaries = (*subsvc.Service)(nil)
	_ notifysvc.Admins                = (*authsvc.Service)(nil)
	_ supportsvc.Settings             = (*settingssvc.Service)(nil)
	_ supportsvc.AdminTicketNotifier  = supportAdminNotifier{}
	_ contentsvc.Site                 = (*settingssvc.Service)(nil)
	_ contentsvc.SubscribeLinks       = contentSubscribeLinks{}
)
