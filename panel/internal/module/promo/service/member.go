package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/promo/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 会员折扣的审计动作名与资源类型。
const (
	ActionMemberDiscountSet     = "member_discount.set"
	ActionMemberDiscountCleared = "member_discount.cleared"

	ResourceUser = "user"
)

// MemberDiscountView 是后台看到的一个用户的会员折扣；Percent 为 nil 表示没有设置。
type MemberDiscountView struct {
	UserID    uuid.UUID
	Percent   *int
	UpdatedBy *uuid.UUID
	UpdatedAt *time.Time
}

// MemberPercent 查询单个用户的会员折扣百分比；订单模块报价、建单时调；没有设置时回 0，不加锁。
func (s *Service) MemberPercent(ctx context.Context, userID uuid.UUID) (int, error) {
	row, ok, err := s.repo.MemberDiscountByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return row.Percent, nil
}

// MemberDiscountOf 查询单个用户的会员折扣设置，供后台查看；用户不存在时回 errs.NotFound。
func (s *Service) MemberDiscountOf(ctx context.Context, userID uuid.UUID) (MemberDiscountView, error) {
	users, err := s.needUsers()
	if err != nil {
		return MemberDiscountView{}, err
	}
	emails, err := users.Emails(ctx, []uuid.UUID{userID})
	if err != nil {
		return MemberDiscountView{}, err
	}
	if _, exists := emails[userID]; !exists {
		return MemberDiscountView{}, errs.New(errs.NotFound)
	}

	row, ok, err := s.repo.MemberDiscountByUser(ctx, userID)
	if err != nil {
		return MemberDiscountView{}, err
	}
	if !ok {
		return MemberDiscountView{UserID: userID}, nil
	}
	return MemberDiscountView{
		UserID:    row.UserID,
		Percent:   &row.Percent,
		UpdatedBy: row.UpdatedBy,
		UpdatedAt: &row.UpdatedAt,
	}, nil
}

// SetMemberDiscount 设置单个用户的会员折扣；percent 必须在 1 到 100 之间。
func (s *Service) SetMemberDiscount(ctx context.Context, userID uuid.UUID, percent int) (MemberDiscountView, error) {
	if percent < 1 || percent > MemberPercentMax {
		return MemberDiscountView{}, errs.Invalid(map[string]string{"percent": "请填 1 到 100 的整数（10 表示打九折）"})
	}
	users, err := s.needUsers()
	if err != nil {
		return MemberDiscountView{}, err
	}
	emails, err := users.Emails(ctx, []uuid.UUID{userID})
	if err != nil {
		return MemberDiscountView{}, err
	}
	if _, exists := emails[userID]; !exists {
		return MemberDiscountView{}, errs.New(errs.NotFound)
	}

	var out MemberDiscountView
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// 锁住旧行再写，审计里的 before 才准；还没有行时锁不到东西，两个并发首次设置靠 upsert 的 ON CONFLICT 收敛，此时 before 都记 nil（金额不受影响）。
		old, ok, err := s.repo.LockMemberDiscountByUser(ctx, userID)
		if err != nil {
			return err
		}
		var before *int
		if ok {
			b := old.Percent
			before = &b
		}

		updatedBy := actorID(ctx)
		row, err := s.repo.UpsertMemberDiscount(ctx, userID, percent, updatedBy)
		if err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.New(errs.NotFound)
			}
			return err
		}

		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionMemberDiscountSet,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before": before,
				"after":  percent,
			},
		}); err != nil {
			return err
		}

		out = MemberDiscountView{
			UserID:    row.UserID,
			Percent:   &row.Percent,
			UpdatedBy: row.UpdatedBy,
			UpdatedAt: &row.UpdatedAt,
		}
		return nil
	})
	if err != nil {
		return MemberDiscountView{}, err
	}
	return out, nil
}

// ClearMemberDiscount 清除单个用户的会员折扣；本来就没有时什么都不写、照样回 nil（幂等）。
func (s *Service) ClearMemberDiscount(ctx context.Context, userID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		old, ok, err := s.repo.LockMemberDiscountByUser(ctx, userID)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		deleted, err := s.repo.DeleteMemberDiscount(ctx, userID)
		if err != nil {
			return err
		}
		if !deleted {
			return nil
		}

		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionMemberDiscountCleared,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before": old.Percent,
			},
		})
	})
}
