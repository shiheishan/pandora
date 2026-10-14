package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
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

func (s *Service) needTelegramUnbinder() (TelegramUnbinder, error) {
	if s.tgUnbind == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.tgUnbind, nil
}
