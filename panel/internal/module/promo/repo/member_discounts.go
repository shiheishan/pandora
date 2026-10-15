package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/promo/model"
)

// MemberDiscountByUser 读单个用户的会员折扣行；没有行 ok 为 false。
func (r *Repo) MemberDiscountByUser(ctx context.Context, userID uuid.UUID) (model.MemberDiscount, bool, error) {
	var m model.MemberDiscount
	err := r.d.Conn(ctx).Where("user_id = ?", userID).Take(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MemberDiscount{}, false, nil
		}
		return model.MemberDiscount{}, false, mapErr(err)
	}
	return m, true, nil
}

// LockMemberDiscountByUser 读单个用户的会员折扣行并加行锁（SELECT … FOR UPDATE），必须在事务里调用；没有行 ok 为 false。
func (r *Repo) LockMemberDiscountByUser(ctx context.Context, userID uuid.UUID) (model.MemberDiscount, bool, error) {
	var m model.MemberDiscount
	err := r.d.Conn(ctx).Where("user_id = ?", userID).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&m).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MemberDiscount{}, false, nil
		}
		return model.MemberDiscount{}, false, mapErr(err)
	}
	return m, true, nil
}

// UpsertMemberDiscount 写入或覆盖单个用户的会员折扣；返回写后的行。
func (r *Repo) UpsertMemberDiscount(ctx context.Context, userID uuid.UUID, percent int, updatedBy *uuid.UUID) (model.MemberDiscount, error) {
	var m model.MemberDiscount
	err := r.d.Conn(ctx).Raw(
		`INSERT INTO member_discounts (user_id, percent, updated_by, created_at, updated_at)
		 VALUES (?, ?, ?, now(), now())
		 ON CONFLICT (user_id) DO UPDATE
		 SET percent = EXCLUDED.percent,
		     updated_by = EXCLUDED.updated_by,
		     updated_at = now()
		 RETURNING user_id, percent, updated_by, created_at, updated_at`,
		userID, percent, updatedBy,
	).Scan(&m).Error
	if err != nil {
		return model.MemberDiscount{}, mapErr(err)
	}
	return m, nil
}

// DeleteMemberDiscount 删除单个用户的会员折扣行；返回是否删到了行。
func (r *Repo) DeleteMemberDiscount(ctx context.Context, userID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Where("user_id = ?", userID).Delete(&model.MemberDiscount{})
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected > 0, nil
}
