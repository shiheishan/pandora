package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// 后台填写原因的字数范围（按字符计）。
const (
	reasonMinRunes = 5
	reasonMaxRunes = 500
)

// errOrderExpired 是取消一张已超时的订单（文案沿用旧 release.go）。
var errOrderExpired = errs.Newf(errs.Conflict, "订单已过期")

// checkReason 去首尾空白后校验 5–500 字。
func checkReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	n := utf8.RuneCountInString(r)
	if n < reasonMinRunes || n > reasonMaxRunes || hasNUL(r) {
		return "", errs.Invalid(map[string]string{"reason": "请填写 5–500 字的原因"})
	}
	return r, nil
}

// Cancel 取消待付订单并释放它占的库存、限购、优惠券、余额冻结与支付意图。
// 门户只能取消自己的单，原因固定 user_cancelled；后台要带 expected_state_version 与 5–500 字原因。
// 已取消的单重复取消回 AlreadyTerminal（旧代码同样先看终态、再比版本）。
func (s *Service) Cancel(ctx context.Context, in CancelInput) (CancelResult, error) {
	var reason string
	p, err := principal(ctx)
	if err != nil {
		return CancelResult{}, err
	}
	if in.Admin {
		if p, err = adminPrincipal(ctx); err != nil {
			return CancelResult{}, err
		}
		if reason, err = checkReason(in.Reason); err != nil {
			return CancelResult{}, err
		}
		if in.ExpectedStateVersion == nil {
			return CancelResult{}, errs.Invalid(map[string]string{"expected_state_version": "必填"})
		}
	} else {
		reason = ReasonUserCancelled
	}

	var out CancelResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		var o model.Order
		var ok bool
		var err error
		if in.Admin {
			o, ok, err = s.repo.OrderByID(ctx, in.OrderID, repo.ForNoKeyUpdate)
		} else {
			o, ok, err = s.repo.OrderForUser(ctx, in.OrderID, p.UserID, repo.ForNoKeyUpdate)
		}
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		switch o.Status {
		case StatusCancelled:
			out = CancelResult{Order: toRow(o), AlreadyTerminal: true}
			return nil
		case StatusExpired:
			return errOrderExpired
		case StatusPaid, StatusFulfilled:
			return ErrAlreadyPaid
		case StatusPending:
		default:
			return errs.Newf(errs.Conflict, "订单当前状态（"+o.Status+"）不能取消")
		}
		if in.Admin && *in.ExpectedStateVersion != o.StateVersion {
			return ErrStateChanged
		}
		released, err := s.releaseOrder(ctx, o, StatusCancelled, reason, EventCancel, actorFrom(p))
		if err != nil {
			return err
		}
		out = CancelResult{Order: toRow(released)}
		return nil
	})
	if err != nil {
		return CancelResult{}, err
	}
	return out, nil
}

// ExpireDue 释放已过付款期限的待付订单：先取一批候选，再每单一个事务（锁单、复核、释放）。
// 单的错误只计数并记日志，不中断这一轮；只有取候选失败才返回 error。
func (s *Service) ExpireDue(ctx context.Context, limit int) (ExpireStats, error) {
	if limit <= 0 {
		limit = ExpireBatchDefault
	}
	if limit > ExpireBatchMax {
		limit = ExpireBatchMax
	}
	now := s.clock()
	var ids []uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		got, err := s.repo.ExpiryCandidates(ctx, now, limit)
		if err != nil {
			return err
		}
		ids = got
		return nil
	})
	if err != nil {
		return ExpireStats{}, err
	}
	stats := ExpireStats{Scanned: len(ids)}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		expired, err := s.expireOne(ctx, id)
		switch {
		case err == nil && expired:
			stats.Expired++
		case err == nil, errors.Is(err, ErrStateChanged):
			stats.Skipped++
		default:
			stats.Failed++
			plog.From(ctx).Warn("订单超时释放失败", zap.String("order_id", id.String()), zap.Error(err))
		}
	}
	return stats, nil
}

// expireOne 在单独的事务里锁单、复核并释放一张超时订单；不该释放时返回 false。
// 订单正被结算或取消锁着时不等锁（SKIP LOCKED），这一轮跳过、计入 Skipped（旧 reservation_expiry 同口径）。
func (s *Service) expireOne(ctx context.Context, id uuid.UUID) (bool, error) {
	expired := false
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		o, ok, err := s.repo.OrderByID(ctx, id, repo.ForNoKeyUpdateSkipLocked)
		if err != nil {
			return err
		}
		now := s.clock()
		if !ok || o.Status != StatusPending || now.Before(o.ExpiresAt) {
			return nil
		}
		if _, err := s.releaseOrder(ctx, o, StatusExpired, ReasonExpired, EventExpire, Actor{Kind: ActorSystem}); err != nil {
			return err
		}
		expired = true
		return nil
	})
	return expired, err
}
