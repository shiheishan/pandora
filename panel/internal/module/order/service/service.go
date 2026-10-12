// Package service 是 order 模块的业务层：报价、下单（新购、续费、流量包、充值）、结算编排（实现 payment 的 Settler）、
// 取消与超时释放、我的订单与后台订单。别的模块只调本包。
//
// 锁序（全模块同向，避免 40P01）：订单 → 订阅 → 套餐行 → 限购计数 → 券 → 账务科目（用户余额、冻结、平台收入、渠道）。
// 建单没有订单锁：续费、流量包先锁订阅，新购先锁套餐行（catalog.Offer Lock）；结算、取消、超时先锁订单。
// 结算先确认预留（库存、限购、券）再记账，见 SettlePayment。
// 下单事务里一律先插订单行，再调 promo.Reserve、ledger.HoldForOrder 等（它们的 order_id 有外键）。
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Catalog 是 catalog service 用到的面。
type Catalog interface {
	Offer(ctx context.Context, priceID uuid.UUID, req catalogsvc.OfferRequest) (catalogsvc.Offer, error)
	PlanOffers(ctx context.Context, planID uuid.UUID, req catalogsvc.OfferRequest) (catalogsvc.OfferPlan, error)
	ListOffers(ctx context.Context, req catalogsvc.OfferRequest) ([]catalogsvc.OfferPlan, error)
	// GetPlan 是后台套餐详情（含全部价格档）：人工开单预览用它确认套餐存在、取价格档
	GetPlan(ctx context.Context, planID uuid.UUID) (catalogsvc.PlanDetail, error)
	PackOffer(ctx context.Context, packID uuid.UUID, lock bool) (catalogsvc.TrafficPack, error)
	ReserveStock(ctx context.Context, planID uuid.UUID, qty int) (bool, error)
	ReleaseStock(ctx context.Context, planID uuid.UUID, qty int) error
	CaptureStock(ctx context.Context, planID uuid.UUID, qty int) error
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
	VersionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Version, error)
}

// Ledger 是 ledger service 用到的面。
type Ledger interface {
	Balance(ctx context.Context, userID uuid.UUID, cur money.Currency) (decimal.Decimal, error)
	HoldForOrder(ctx context.Context, in ledgersvc.HoldInput) (ledgersvc.Hold, error)
	ReleaseOrderHold(ctx context.Context, orderID uuid.UUID, memo string) (bool, error)
	CaptureFromBalance(ctx context.Context, in ledgersvc.HoldInput) (ledgersvc.Hold, uuid.UUID, error)
	PostOrderPaid(ctx context.Context, p ledgersvc.OrderPaid) (uuid.UUID, error)
	Post(ctx context.Context, p ledgersvc.Posting) (uuid.UUID, error)
}

// Promo 是 promo service 用到的面。
type Promo interface {
	Evaluate(ctx context.Context, code string, t promosvc.Target) (*promosvc.Applied, error)
	Reserve(ctx context.Context, code string, t promosvc.Target, o promosvc.OrderRef) (*promosvc.Applied, error)
	Release(ctx context.Context, orderID uuid.UUID, reason string) error
	Capture(ctx context.Context, orderID uuid.UUID) error
	Codes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Subscriptions 是 subscription service 用到的面。
type Subscriptions interface {
	Get(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (subsvc.Subscription, error)
	Lock(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (subsvc.Subscription, error)
	ByUser(ctx context.Context, userID uuid.UUID) (subsvc.UserSubscriptions, error)
	RenewableForPlan(ctx context.Context, userID, planID uuid.UUID, lock bool) (subsvc.Subscription, bool, error)
	CheckPackTarget(ctx context.Context, userID, subID uuid.UUID) error
	Provision(ctx context.Context, in subsvc.Provision) (subsvc.Subscription, error)
	Renew(ctx context.Context, in subsvc.Renewal) (subsvc.RenewalResult, error)
	GrantTrafficPack(ctx context.Context, in subsvc.PackGrant) (subsvc.Pack, error)
	ChangePlan(ctx context.Context, in subsvc.PlanChange) (subsvc.PlanChangeResult, error)
	Quotas(ctx context.Context, subID uuid.UUID) ([]subsvc.Quota, error)
	// PeriodOrderIDsBySubscriptions 是每份订阅本周期的付费来源订单（按发生先后）：换套餐折算剩余价值的基数
	PeriodOrderIDsBySubscriptions(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
}

// Payments 是 payment service 用到的面。
type Payments interface {
	CreateIntent(ctx context.Context, in paymentsvc.CreateIntentInput) (paymentsvc.IntentResult, error)
	CloseOrderIntents(ctx context.Context, orderID uuid.UUID, to paymentsvc.IntentStatus) (int64, error)
	RecordOffline(ctx context.Context, in paymentsvc.OfflineInput) (paymentsvc.RecordResult, error)
	Query(ctx context.Context, orderID uuid.UUID) (paymentsvc.QueryResult, error)
	OrderHistory(ctx context.Context, orderID uuid.UUID) (paymentsvc.OrderHistory, error)
	OrderSummaries(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID]paymentsvc.OrderPaymentSummary, error)
	MinPayment(ctx context.Context, cur money.Currency) (decimal.Decimal, error)
}

// Users 是 identity service 用到的面。
type Users interface {
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	IDsByEmail(ctx context.Context, q string, limit int) ([]uuid.UUID, error)
}

// idempotency 是 idempotency service 的业务事务内完成。
type idempotency interface {
	CompleteBound(ctx context.Context, c *idemsvc.Claim, r idemsvc.Response, res idemsvc.Resource) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// Service 是 order 模块对外的业务入口；同时实现 paymentsvc.Settler。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	catalog  Catalog
	ledger   Ledger
	promo    Promo
	subs     Subscriptions
	payments Payments
	users    Users
	idem     idempotency
	audit    auditWriter
	hook     PaidHook // UsePaidHook 注入，可为 nil
	now      func() time.Time
}

var _ paymentsvc.Settler = (*Service)(nil)

// New 构造 order service。构造后由 app 调 paymentSvc.UseSettler(orderSvc)。
func New(r *repo.Repo, txm *tx.Manager, catalog *catalogsvc.Service, ledger *ledgersvc.Service,
	promo *promosvc.Service, subs *subsvc.Service, payments *paymentsvc.Service,
	users *identitysvc.Service, idem *idemsvc.Service, audit *auditsvc.Service) *Service {
	return &Service{
		repo:     r,
		tx:       txm,
		catalog:  catalog,
		ledger:   ledger,
		promo:    promo,
		subs:     subs,
		payments: payments,
		users:    users,
		idem:     idem,
		audit:    audit,
		now:      time.Now,
	}
}

// UsePaidHook 注册结算成功回调（commission）；只在启动装配时调用（不并发安全）。
func (s *Service) UsePaidHook(h PaidHook) { s.hook = h }

// clock 取当前时间（UTC）。
func (s *Service) clock() time.Time { return s.now().UTC() }

// orderTransitions 是订单状态的转换表。paid 只在结算事务里短暂停留，随即转 fulfilled。
var orderTransitions = map[string][]string{
	StatusPending: {StatusPaid, StatusCancelled, StatusExpired},
	StatusPaid:    {StatusFulfilled},
}

// CanTransition 查订单状态转换表。
func CanTransition(from, to string) bool {
	for _, t := range orderTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// reservationTransitions 是预留状态的转换表：held 只走一次，到 captured 或 released。
var reservationTransitions = map[string][]string{
	ReservationHeld: {ReservationCaptured, ReservationReleased},
}

// CanTransitionReservation 查预留状态转换表。
func CanTransitionReservation(from, to string) bool {
	for _, t := range reservationTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// IsTerminal 表示订单已不会再变。
func IsTerminal(status string) bool {
	return status == StatusFulfilled || status == StatusCancelled || status == StatusExpired
}

// completeCreate 在建单事务里把幂等认领完成并绑定到订单；ctx 里没有认领（测试、内部调用）时什么都不做。
func (s *Service) completeCreate(ctx context.Context, orderID uuid.UUID, build func() (idemsvc.Response, error)) error {
	claim, ok := idemsvc.ClaimFrom(ctx)
	if !ok || build == nil {
		return nil
	}
	resp, err := build()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	return s.idem.CompleteBound(ctx, claim, resp, idemsvc.Resource{Type: ResourceOrder, ID: orderID})
}

// principal 取 ctx 里的登录主体；没有主体回 401。
func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
}

// portalUser 取门户登录用户的 ID（门户接口用；后台会话也有 UserID，但门户路由只挂门户鉴权）。
func portalUser(ctx context.Context) (uuid.UUID, error) {
	p, err := principal(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return p.UserID, nil
}

// adminPrincipal 取后台登录主体；不是后台会话回 403。
func adminPrincipal(ctx context.Context) (authn.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, err
	}
	if p.Audience != authn.AudienceAdmin {
		return p, errs.New(errs.Forbidden)
	}
	return p, nil
}

// actorFrom 把登录主体转成预留流水上的操作者（后台 admin、门户 user）。
func actorFrom(p authn.Principal) Actor {
	id := p.UserID
	if p.Audience == authn.AudienceAdmin {
		return Actor{Kind: ActorAdmin, ID: &id}
	}
	return Actor{Kind: ActorUser, ID: &id}
}
