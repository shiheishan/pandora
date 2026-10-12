// Package service 是 giftcard 模块的业务层：礼品卡模板（含奖池与领取条件）、生码批次与一次性导出、卡密列表与启停、
// 掩码报表、统计与使用明细，以及门户的兑换预览、兑换与我的兑换记录。
//
// 卡密是等价现金，库里不存明文：只存 HMAC（hashCode）与掩码；整批明文封存在批次上，一次性导出时清空。
// 完整卡密只在两处出站：生码响应里的 4 张样例（不进幂等重放）、批次的一次性导出（不进幂等表）。
//
// 兑换在一个事务里：锁码 → 校验状态与有效期 → 校验模板、领取条件与每人限次 → 先把码 unused → used（防重）
// → 发放（经 subscription、ledger 的 service）→ 写兑换流水 → 审计 → 幂等完成。发放方法本身不按来源防重，
// 防重靠这一步的状态转换（WHERE status = 'unused'）与兑换流水上 code_id 的唯一约束。
// 对外所有「码不能用」的情况回同一句话（ErrCodeUnusable），不让人借错误文案区分码是否存在。
package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/giftcard/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
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

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// idempotency 是幂等 service 在业务事务内完成认领的那几个方法；*idemsvc.Service 直接满足。
type idempotency interface {
	Complete(ctx context.Context, c *idemsvc.Claim, r idemsvc.Response) error
	CompleteBound(ctx context.Context, c *idemsvc.Claim, r idemsvc.Response, res idemsvc.Resource) error
	CompleteUnreplayable(ctx context.Context, c *idemsvc.Claim, status int) error
}

// Catalog 是 giftcard 要的目录读取面；*catalogsvc.Service 直接满足。
type Catalog interface {
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
	PricesByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Price, error)
}

// Subscriptions 是 giftcard 发放要用的订阅写入与读取面；*subsvc.Service 直接满足。
// 写方法都加入调用方事务、自己锁订阅行并复核状态。
type Subscriptions interface {
	ByUser(ctx context.Context, userID uuid.UUID) (subsvc.UserSubscriptions, error)
	Lock(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (subsvc.Subscription, error)
	Provision(ctx context.Context, in subsvc.Provision) (subsvc.Subscription, error)
	Renew(ctx context.Context, in subsvc.Renewal) (subsvc.RenewalResult, error)
	Extend(ctx context.Context, in subsvc.Extension) (subsvc.ExtensionResult, error)
	ChangePlan(ctx context.Context, in subsvc.PlanChange) (subsvc.PlanChangeResult, error)
	GrantTrafficPack(ctx context.Context, in subsvc.PackGrant) (subsvc.Pack, error)
	ResetTraffic(ctx context.Context, in subsvc.TrafficReset) (int64, error)
}

// Ledger 是 giftcard 要的记账面；*ledgersvc.Service 直接满足。
type Ledger interface {
	Post(ctx context.Context, p ledgersvc.Posting) (uuid.UUID, error)
	DefaultCurrency(ctx context.Context) (money.Currency, error)
}

// Users 是 giftcard 要的用户读取面；*identitysvc.Service 直接满足。
type Users interface {
	// Emails 批量取用户邮箱（一条 SQL）。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// Hasher 是卡密的 HMAC；*crypto.Keys 直接满足。
type Hasher interface {
	HMAC(info string, data []byte) []byte
}

// Sealer 封存与打开整批明文卡密；*crypto.Sealer 直接满足。
type Sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// PlanChangeValuer 由 order 实现（换套餐折算的唯一出处）：一份订阅此刻没用完的已付价值、币种，
// 以及换掉时不保留的赠送整天数。套餐卡「换掉那一份」时把这笔价值全额退进余额。
// 没注册时套餐卡不给「换掉」的选项（只能续同款或另开一份），免得用户的已付价值被吞掉。
// 这份订阅有没完结的续费或换套餐单时回 order 的 ErrOpenChange（errs.Conflict）：兑换原样往上抛、卡不会被用掉，
// 预览不给这一份的「换掉」。giftcard 在码转 used 之前、锁住订阅之后调它。
type PlanChangeValuer interface {
	RemainingValue(ctx context.Context, subscriptionID uuid.UUID, now time.Time) (amount decimal.Decimal, currency money.Currency, giftDaysLost int, err error)
}

// PaidHistory 由 order 实现：用户有没有付过钱（实付大于 0、已支付或已履约）的订单。「仅新用户」「仅付费用户」两个条件用它。
type PaidHistory interface {
	HasPaidOrder(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Referrals 由 identity 实现：用户是不是经邀请注册的。「仅受邀用户」条件用它。
type Referrals interface {
	HasReferrer(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Service 是 giftcard 模块对外的唯一入口。
type Service struct {
	repo    *repo.Repo
	tx      txRunner
	audit   auditWriter
	idem    idempotency
	catalog Catalog
	subs    Subscriptions
	ledger  Ledger
	users   Users
	hasher  Hasher
	sealer  Sealer
	valuer  PlanChangeValuer
	paid    PaidHistory
	refs    Referrals
	now     func() time.Time
}

// New 构造 giftcard service。catalog 是 catalog service，subs 是 subscription service，ledger 是 ledger service，
// users 是 identity service，hasher 是 crypto.Keys，sealer 是 crypto.Keys.Sealer()。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, idem *idemsvc.Service, catalog Catalog,
	subs Subscriptions, ledger Ledger, users Users, hasher Hasher, sealer Sealer) *Service {
	return &Service{
		repo:    r,
		tx:      txm,
		audit:   audit,
		idem:    idem,
		catalog: catalog,
		subs:    subs,
		ledger:  ledger,
		users:   users,
		hasher:  hasher,
		sealer:  sealer,
		now:     time.Now,
	}
}

// UsePlanChangeValuer 注册换套餐折算（order）；只在启动装配时调用（不并发安全）。
func (s *Service) UsePlanChangeValuer(v PlanChangeValuer) { s.valuer = v }

// UsePaidHistory 注册付费记录查询（order）；只在启动装配时调用。
func (s *Service) UsePaidHistory(p PaidHistory) { s.paid = p }

// UseReferrals 注册邀请关系查询（identity）；只在启动装配时调用。
func (s *Service) UseReferrals(r Referrals) { s.refs = r }

// MissingPorts 列出还没注册的可选依赖；app 装配完调用，非空就拒绝启动（缺了它们对应的卡会一直回 503 或少了「换掉」）。
func (s *Service) MissingPorts() []string {
	var out []string
	if s.valuer == nil {
		out = append(out, "PlanChangeValuer")
	}
	if s.paid == nil {
		out = append(out, "PaidHistory")
	}
	if s.refs == nil {
		out = append(out, "Referrals")
	}
	return out
}

// clock 返回当前时刻（UTC、截到微秒，与库里的精度一致）。
func (s *Service) clock() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// 审计动作名（沿用旧代码）与资源类型。
const (
	ActionTemplateCreated = "gift_card_template.created"
	ActionTemplateUpdated = "gift_card_template.updated"
	ActionCodesGenerated  = "gift_card.codes_generated"
	ActionCodeToggled     = "gift_card.code_toggled"
	ActionReportExported  = "gift_card.codes_report_exported"
	ActionBatchExported   = "gift_card.batch_exported"
	ActionRedeemed        = "gift_card.redeemed"
	ResourceTemplate      = "gift_card_template"
	ResourceCode          = "gift_card_code"
	ResourceBatch         = "gift_card_batch"
)

// errSealedMismatch 是批次密文里的码在码表里找不到（封存与码表对不上，程序缺陷）。
var errSealedMismatch = errors.New("giftcard: 批次密文与码表对不上")

// 对外的固定错误。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	// ErrCodeUnusable 对所有「这个码不能用」的情况回同一句话：不存在、已用、已停用、已过期、模板停用都是它，
	// 免得爆破的人靠错误文案分辨哪些码真实存在。
	ErrCodeUnusable = errs.Newf(errs.ValidationFailed, "卡密无效、已被使用或已过期")
	// ErrTemplateInactive 是码本身可用、但所属活动已暂停或归档（只在兑换时给出，预览一律回 ErrCodeUnusable）。
	ErrTemplateInactive = errs.Newf(errs.ValidationFailed, "这个礼品卡活动已经停用了")
	// ErrNoPlacement 是这张卡要落到一份在用的套餐上，而用户一份都没有。
	ErrNoPlacement = errs.Newf(errs.ValidationFailed, "现在没有在用的套餐，这张卡暂时用不了。先续费再来兑换，卡不会过期")
	// ErrChoiceStale 是选定的用法不在当前选项里（订阅状态在预览之后变了，或请求被篡改）。
	ErrChoiceStale = errs.Invalid(map[string]string{"choice": "这个用法现在不能用了，请刷新后再选"})
	// ErrChoiceRequired 是选项多于一个却没有选。
	ErrChoiceRequired = errs.Invalid(map[string]string{"choice": "请先选一种用法"})
	// ErrBatchAlreadyExported 是一次性导出的第二次请求。
	ErrBatchAlreadyExported = errs.Newf(errs.Conflict, "该批次已导出，完整卡码不可再次获取")
	// ErrConditionUnavailable 是模板的领取条件要查的数据源没装配（付费记录、邀请关系）：宁可不让兑，也不放过条件。
	ErrConditionUnavailable = errs.Newf(errs.Unavailable, "暂时无法核对这张卡的领取条件，请稍后再试")
	// ErrStateChanged 是带旧状态的条件更新影响 0 行（并发改了状态）。
	ErrStateChanged = errs.Newf(errs.Conflict, "状态已变化，请刷新后重试")
)

// principal 取 ctx 里的登录主体；没有主体回 401。
func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
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

// portalUser 取门户登录用户；不是门户会话回 403。
func portalUser(ctx context.Context) (uuid.UUID, error) {
	p, err := principal(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	if p.Audience != authn.AudiencePublic {
		return uuid.Nil, errs.New(errs.Forbidden)
	}
	return p.UserID, nil
}

// writeAudit 在 ctx 的事务里写一条审计（操作者由 audit 从登录主体取）；Data 不放卡密。
func (s *Service) writeAudit(ctx context.Context, action, resourceType string, resourceID *uuid.UUID, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Data:         data,
	})
}

// gcErr 把 repo 的约束哨兵转成对外错误；已是 *errs.Error 的原样返回。
func gcErr(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, repo.ErrDuplicate):
		return errs.Newf(errs.Conflict, "记录已存在")
	case errors.Is(err, repo.ErrForeignKey):
		return errs.Invalid(map[string]string{"reference": "关联对象不存在"})
	case errors.Is(err, repo.ErrCheck):
		return errs.Newf(errs.ValidationFailed, "礼品卡数据校验未通过")
	case errors.Is(err, repo.ErrAppendOnly):
		return errs.Wrap(errs.Internal, err)
	}
	return err
}
