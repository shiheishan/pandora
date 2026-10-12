package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// CreateSubscription 插入一份订阅，并回填库里生成的列（id、proxy_uuid、node_uid、时间戳）。
func (r *Repo) CreateSubscription(ctx context.Context, s *model.Subscription) error {
	conn := r.d.Conn(ctx)
	if err := conn.Create(s).Error; err != nil {
		return mapErr(err)
	}
	return mapErr(conn.Raw("SELECT * FROM subscriptions WHERE id = ?", s.ID).Scan(s).Error)
}

// SubscriptionByID 按 id 读一份订阅（可加行锁）；不存在返回 false。
func (r *Repo) SubscriptionByID(ctx context.Context, id uuid.UUID, lock Lock) (model.Subscription, bool, error) {
	var s model.Subscription
	err := r.d.Conn(ctx).Raw("SELECT * FROM subscriptions WHERE id = ?"+lock.suffix(), id).Scan(&s).Error
	if err != nil {
		return model.Subscription{}, false, mapErr(err)
	}
	if s.ID == uuid.Nil {
		return model.Subscription{}, false, nil
	}
	return s, true, nil
}

// SubscriptionsByIDs 批量读订阅（不加锁、不保证顺序）。
func (r *Repo) SubscriptionsByIDs(ctx context.Context, ids []uuid.UUID) ([]model.Subscription, error) {
	if len(ids) == 0 {
		return []model.Subscription{}, nil
	}
	var out []model.Subscription
	err := r.d.Conn(ctx).Where("id IN ?", ids).Find(&out).Error
	return out, mapErr(err)
}

// SubscriptionsByUsers 批量读一组用户的全部订阅，按用户、创建时间倒序。
func (r *Repo) SubscriptionsByUsers(ctx context.Context, userIDs []uuid.UUID) ([]model.Subscription, error) {
	if len(userIDs) == 0 {
		return []model.Subscription{}, nil
	}
	var out []model.Subscription
	err := r.d.Conn(ctx).Where("user_id IN ?", userIDs).
		Order("user_id, created_at DESC, id DESC").Find(&out).Error
	return out, mapErr(err)
}

// SubscriptionsByUserPlan 读一个用户在某个套餐下的全部订阅，创建时间倒序。
func (r *Repo) SubscriptionsByUserPlan(ctx context.Context, userID, planID uuid.UUID) ([]model.Subscription, error) {
	var out []model.Subscription
	err := r.d.Conn(ctx).Where("user_id = ? AND plan_id = ?", userID, planID).
		Order("created_at DESC, id DESC").Find(&out).Error
	return out, mapErr(err)
}

// UpdateSubscription 带旧状态条件更新订阅的若干列；返回是否恰好改到一行。
func (r *Repo) UpdateSubscription(ctx context.Context, id uuid.UUID, fromStatus string, cols map[string]any) (bool, error) {
	cols["updated_at"] = now()
	res := r.d.Conn(ctx).Model(&model.Subscription{}).
		Where("id = ? AND status = ?", id, fromStatus).Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SetLabel 改订阅的备注名（nil 清空）。撞同一用户的备注名唯一索引时只回滚这一步并返回 false；
// 订阅不存在或不属于该用户也返回 false。只在事务里调用。
func (r *Repo) SetLabel(ctx context.Context, id, userID uuid.UUID, label *string) (bool, error) {
	conn := r.d.Conn(ctx)
	const sp = "sub_label"
	if err := conn.SavePoint(sp).Error; err != nil {
		return false, err
	}
	res := conn.Model(&model.Subscription{}).Where("id = ? AND user_id = ?", id, userID).
		Updates(map[string]any{"label": label, "updated_at": now()})
	if res.Error != nil {
		// 这条 UPDATE 只改 label 与 updated_at，唯一冲突只可能来自同一用户的备注名唯一索引。
		if isDuplicate(res.Error) {
			if err := conn.RollbackTo(sp).Error; err != nil {
				return false, err
			}
			return false, nil
		}
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SubscriptionByLabel 找同一用户下备注名相同（不分大小写）的另一份订阅。
func (r *Repo) SubscriptionByLabel(ctx context.Context, userID uuid.UUID, label string, excludeID uuid.UUID) (model.Subscription, bool, error) {
	var s model.Subscription
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM subscriptions WHERE user_id = ? AND id <> ? AND lower(label) = lower(?) LIMIT 1",
		userID, excludeID, label,
	).Scan(&s).Error
	if err != nil {
		return model.Subscription{}, false, mapErr(err)
	}
	if s.ID == uuid.Nil {
		return model.Subscription{}, false, nil
	}
	return s, true, nil
}

// InsertEvent 追加一条订阅事件。
func (r *Repo) InsertEvent(ctx context.Context, e *model.Event) error {
	return mapErr(r.d.Conn(ctx).Create(e).Error)
}

// InsertCredential 插入一条订阅链接凭据。
func (r *Repo) InsertCredential(ctx context.Context, c *model.Credential) error {
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}
