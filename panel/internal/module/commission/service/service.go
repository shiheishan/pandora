// Package service 是 commission 模块的业务层：订单结算时计提佣金、冻结期满解冻、提现申请与审核打款、佣金转余额、
// 后台概览与参数、门户我的佣金。别的模块只调本包。
//
// 三个阶段都在账本上留痕：
//
//	计提  订单结算 → 借 平台收入 / 贷 推荐人待结佣金（commission_accrued）
//	解冻  冻结期满 → 借 待结佣金 / 贷 可提现佣金（commission_settled）
//	打款  提现完成 → 借 可提现佣金 / 贷 打款渠道资金（commission_paid）
//	作废  复核作废 → 借 待结佣金 / 贷 平台收入（commission_voided，冲回计提；条目转 voided）
//
// 转余额：借 可提现佣金 / 贷 用户余额（commission_to_balance）。提现申请与审批不记账，只改状态。
package service

import (
	"context"
	"errors"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// Ledger 是 commission 用到的 ledger service 方法；*ledgersvc.Service 实现它。
type Ledger interface {
	Post(ctx context.Context, p ledgersvc.Posting) (uuid.UUID, error)
	DefaultCurrency(ctx context.Context) (money.Currency, error)
}

// Settings 是 commission 用到的 settings service 方法；*settingssvc.Service 实现它。
type Settings interface {
	Commission(ctx context.Context) (settingssvc.Commission, error)
	SetCommission(ctx context.Context, u settingssvc.CommissionUpdate) error
	Site(ctx context.Context) (settingssvc.Site, error)
}

// Referrals 是推荐关系与用户邮箱的读取面，由 identity 实现（UseReferrals 注入）。
type Referrals interface {
	// ReferrerOf 返回用户的推荐人；没有推荐人时返回 nil。
	ReferrerOf(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)
	// InviteeCount 返回某人邀请来的人数。
	InviteeCount(ctx context.Context, referrerID uuid.UUID) (int64, error)
	// ReferralCount 返回全站推荐关系总数。
	ReferralCount(ctx context.Context) (int64, error)
	// Emails 批量取用户邮箱（一条 SQL）；查不到的 ID 不在结果里。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// SourceMatcher 判断两个用户是否来自同一来源地址，由 audit 实现（UseSourceMatcher 注入）；命中时佣金转人工复核、不自动解冻。
// 没注入或查询出错时同样转人工复核（失败即关）。
type SourceMatcher interface {
	SharesSource(ctx context.Context, a, b uuid.UUID) (bool, error)
}

// Service 是 commission 模块对外的业务入口；同时实现 ordersvc.PaidHook。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	ledger   Ledger
	settings Settings
	sealer   *crypto.Sealer
	audit    auditWriter
	refs     Referrals     // UseReferrals 注入
	sources  SourceMatcher // UseSourceMatcher 注入；为 nil 时每条佣金都转人工复核
	now      func() time.Time
}

var _ ordersvc.PaidHook = (*Service)(nil)

// New 构造 commission service。构造后由 app 调 UseReferrals(identity)、UseSourceMatcher(audit) 与 orderSvc.UsePaidHook(commissionSvc)。
func New(r *repo.Repo, txm *tx.Manager, ledger *ledgersvc.Service, settings *settingssvc.Service,
	sealer *crypto.Sealer, audit *auditsvc.Service) *Service {
	return &Service{
		repo:     r,
		tx:       txm,
		ledger:   ledger,
		settings: settings,
		sealer:   sealer,
		audit:    audit,
		now:      time.Now,
	}
}

// UseReferrals 注入推荐关系读取面；只在启动装配时调用（不并发安全）。
func (s *Service) UseReferrals(r Referrals) { s.refs = r }

// UseSourceMatcher 注入同源判断；只在启动装配时调用（不并发安全）。
func (s *Service) UseSourceMatcher(m SourceMatcher) { s.sources = m }

// MissingPorts 列出还没注册的必需依赖；装配完成后由 internal/app 统一检查，非空就拒绝启动
// （缺 Referrals 时结算全部报错，缺 SourceMatcher 时每条佣金都转人工复核）。
func (s *Service) MissingPorts() []string {
	var out []string
	if s.refs == nil {
		out = append(out, "Referrals")
	}
	if s.sources == nil {
		out = append(out, "SourceMatcher")
	}
	return out
}

var errNotWired = errors.New("commission: 依赖未注入")

func (s *Service) needRefs() (Referrals, error) {
	if s.refs == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.refs, nil
}

// clock 取当前时间（UTC）。
func (s *Service) clock() time.Time { return s.now().UTC() }

// principalID 取 ctx 里登录主体的用户 ID；没有主体回 401。
func principalID(ctx context.Context) (uuid.UUID, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return uuid.Nil, errs.New(errs.Unauthorized)
	}
	return p.UserID, nil
}

// payoutAAD 是收款信息封存的附加数据：绑定申请人，密文不能挪到别人的提现上解开。
func payoutAAD(userID uuid.UUID) []byte { return []byte("withdrawal.payout:" + userID.String()) }
