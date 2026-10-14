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

// TelegramUnbinder 由 notify 实现（已有的 UnbindTelegram；没绑定回 errs.NotFound，调用方当作成功）。
type TelegramUnbinder interface {
	UnbindTelegram(ctx context.Context, userID uuid.UUID) error
}

// UseTrialGranter 只在启动装配时调用（不并发安全）。
func (s *Service) UseTrialGranter(t TrialGranter) { s.trials = t }

// UseAccountCloser 只在启动装配时调用（不并发安全）。
func (s *Service) UseAccountCloser(c AccountCloser) { s.closer = c }

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

func (s *Service) needTelegramUnbinder() (TelegramUnbinder, error) {
	if s.tgUnbind == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.tgUnbind, nil
}
