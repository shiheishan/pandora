package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TrialGranter 由 subscription 实现。
type TrialGranter interface {
	GrantSignupTrial(ctx context.Context, userID, planID uuid.UUID, hours int) error
}

// AccountCloser 由 subscription 实现：注销时停掉该用户全部订阅，返回停掉的份数。
type AccountCloser interface {
	CloseUserSubscriptions(ctx context.Context, userID uuid.UUID, reason string) (int, error)
}

// OrderCanceller 由 order 实现：注销时在同一事务里取消该用户全部待付订单（释放库存、限购、优惠券、余额冻结，
// 作废在途支付意图），返回取消的张数。之后到的付款回调按「迟到付款」处理（挂起后入余额），不开订阅、不计佣金。
type OrderCanceller interface {
	CancelUserPending(ctx context.Context, userID uuid.UUID) (int, error)
}

// CommissionVoider 由 commission 实现（app 里适配）：注销时在同一事务里作废这个人名下未打款的佣金、驳回申请中的提现（用户 2026-10-14 定；有已批准未打款的提现时回 409，不许注销）。
// 冻结中、待复核的条目逐条作废，申请中的提现驳回后剩下的可提现佣金作废；已打款、已转余额的不动。再调一次返回全 0。
// 锁序见 commission 的 VoidForDeletedUser：锁用户行之后、取消待付单之前调 LockForDeletion（拿佣金咨询锁）；
// 停完订阅之后、改占位邮箱（用户行锁升到 FOR UPDATE）之前调 VoidForDeletedUser。
type CommissionVoider interface {
	LockForDeletion(ctx context.Context, userID uuid.UUID) error
	VoidForDeletedUser(ctx context.Context, userID uuid.UUID) (CommissionVoid, error)
}

// CommissionVoid 是注销时作废佣金的结果。
type CommissionVoid struct {
	VoidedEntries       int             // 作废的冻结中、待复核条目数
	VoidedPending       decimal.Decimal // 这些条目的金额合计
	Forfeited           decimal.Decimal // 作废的可提现佣金（含驳回提现退回的部分）
	RejectedWithdrawals int             // 驳回的申请中提现笔数
	RejectedAmount      decimal.Decimal // 驳回的提现金额合计
}

// TelegramUnbinder 由 notify 实现（已有的 UnbindTelegram；没绑定回 errs.NotFound，调用方当作成功）。
type TelegramUnbinder interface {
	UnbindTelegram(ctx context.Context, userID uuid.UUID) error
}

// UseTrialGranter 只在启动装配时调用（不并发安全）。
func (s *Service) UseTrialGranter(t TrialGranter) { s.trials = t }

// UseAccountCloser 只在启动装配时调用（不并发安全）。
func (s *Service) UseAccountCloser(c AccountCloser) { s.closer = c }

// UseOrderCanceller 只在启动装配时调用（不并发安全）。
func (s *Service) UseOrderCanceller(c OrderCanceller) { s.cancelOrders = c }

// UseCommissionVoider 只在启动装配时调用（不并发安全）。
func (s *Service) UseCommissionVoider(v CommissionVoider) { s.voidCommission = v }

// UseTelegramUnbinder 只在启动装配时调用（不并发安全）。
func (s *Service) UseTelegramUnbinder(u TelegramUnbinder) { s.tgUnbind = u }

func (s *Service) needTrials() (TrialGranter, error) {
	if s.trials == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.trials, nil
}

func (s *Service) needCloser() (AccountCloser, error) {
	if s.closer == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.closer, nil
}

func (s *Service) needOrderCanceller() (OrderCanceller, error) {
	if s.cancelOrders == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.cancelOrders, nil
}

func (s *Service) needCommissionVoider() (CommissionVoider, error) {
	if s.voidCommission == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.voidCommission, nil
}

func (s *Service) needTelegramUnbinder() (TelegramUnbinder, error) {
	if s.tgUnbind == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.tgUnbind, nil
}
