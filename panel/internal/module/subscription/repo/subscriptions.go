package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// now 返回写库用的当前时刻（UTC、截到微秒）。
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// CreateSubscription 插入一份订阅（金额先过 money.Check），并回填库里生成的列（id、proxy_uuid、node_uid、时间戳）。
func (r *Repo) CreateSubscription(ctx context.Context, s *model.Subscription) error {
	if err := money.Check(s.SnapshotAmount); err != nil {
		return err
	}
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

// UpdateSubscription 带旧状态条件更新订阅的若干列（含 snapshot_amount 时先过 money.Check）；返回是否恰好改到一行。
func (r *Repo) UpdateSubscription(ctx context.Context, id uuid.UUID, fromStatus string, cols map[string]any) (bool, error) {
	if v, ok := cols["snapshot_amount"]; ok {
		d, isDec := v.(decimal.Decimal)
		if !isDec {
			return false, fmt.Errorf("subscription: snapshot_amount 必须是 decimal.Decimal，得到 %T", v)
		}
		if err := money.Check(d); err != nil {
			return false, err
		}
	}
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

// PeriodOrderIDsBySubscriptions 批量读订阅本周期的付费来源订单：每份订阅最近一次让周期重新起算的事件（开通、换套餐、
// payload.restart 为真的续期或延期，即过期后续回、救回）及其之后的开通、续期、换套餐事件上挂的订单，按事件先后。
// 重起只认 payload.restart：到期前下单、回调晚到的续费接在原到期之后，不重起，但事件的 from_status 可能已是 expired。
// 事件按 id（uuidv7，插入有序）排：同一订阅的事件由订阅行锁串行写入，插入顺序就是发生顺序（occurred_at 是事务开始时刻，
// 等锁的事务会比先提交的还早，不能用它排）。没有订单的订阅不在结果里。用 Rows 逐行读：GORM 的 Scan 读不进 uuid 切片。
func (r *Repo) PeriodOrderIDsBySubscriptions(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	out := make(map[uuid.UUID][]uuid.UUID, len(subIDs))
	if len(subIDs) == 0 {
		return out, nil
	}
	rows, err := r.d.Conn(ctx).Raw(`
		WITH restart AS (
			SELECT DISTINCT ON (subscription_id) subscription_id, id
			  FROM subscription_events
			 WHERE subscription_id IN ?
			   AND (event_type IN ('activated', 'plan_changed')
			        OR (event_type IN ('renewed', 'extended') AND payload->>'restart' = 'true'))
			 ORDER BY subscription_id, id DESC
		)
		SELECT e.subscription_id, e.order_id
		  FROM subscription_events e
		  JOIN restart r ON r.subscription_id = e.subscription_id AND e.id >= r.id
		 WHERE e.subscription_id IN ?
		   AND e.event_type IN ('activated', 'renewed', 'plan_changed')
		   AND e.order_id IS NOT NULL
		 ORDER BY e.subscription_id, e.id`, subIDs, subIDs).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var subID, orderID uuid.UUID
		if err := rows.Scan(&subID, &orderID); err != nil {
			return nil, mapErr(err)
		}
		out[subID] = append(out[subID], orderID)
	}
	return out, mapErr(rows.Err())
}
