package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// PaymentRow 是带渠道代码与名称的收款。
type PaymentRow struct {
	model.Payment
	ProviderCode string
	ProviderName string
}

// InsertEvent INSERT ... ON CONFLICT DO NOTHING；撞了返回 inserted=false、err=nil，插进去时 ev.ID 已回填。
func (r *Repo) InsertEvent(ctx context.Context, ev *model.Event) (bool, error) {
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "provider_id"}, {Name: "provider_event_id"}},
		DoNothing: true,
	}).Create(ev)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// FinishEvent 把 pending 事件改成 status，processed_at = now()，processing_error = errMsg（nil 即清空）；WHERE processing_status = 'pending'。
func (r *Repo) FinishEvent(ctx context.Context, id uuid.UUID, status string, errMsg *string) (bool, error) {
	updates := map[string]any{
		"processing_status": status,
		"processed_at":      gorm.Expr("now()"),
		"processing_error":  errMsg,
	}
	res := r.d.Conn(ctx).Model(&model.Event{}).Where("id = ? AND processing_status = 'pending'", id).Updates(updates)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// LockPaymentKey 事务级咨询锁：键是 "payment:" + providerID + ":" + providerPaymentID。
func (r *Repo) LockPaymentKey(ctx context.Context, providerID uuid.UUID, providerPaymentID string) error {
	key := "payment:" + providerID.String() + ":" + providerPaymentID
	return r.d.Conn(ctx).Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, key).Error
}

// PaymentByProviderRef 按 (provider_id, provider_payment_id) 读收款。
func (r *Repo) PaymentByProviderRef(ctx context.Context, providerID uuid.UUID, providerPaymentID string) (model.Payment, bool, error) {
	var rows []model.Payment
	if err := r.d.Conn(ctx).Where("provider_id = ? AND provider_payment_id = ?", providerID, providerPaymentID).Limit(1).Find(&rows).Error; err != nil {
		return model.Payment{}, false, err
	}
	if len(rows) == 0 {
		return model.Payment{}, false, nil
	}
	return rows[0], true, nil
}

// InsertPayment 插一行；撞唯一键返回 ErrDuplicate。写前 money.Check(Amount)、money.Check(FeeAmount)。
func (r *Repo) InsertPayment(ctx context.Context, p *model.Payment) error {
	if err := money.Check(p.Amount); err != nil {
		return err
	}
	if err := money.Check(p.FeeAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// PaymentsByOrder 订单的全部收款，JOIN 渠道，按 paid_at DESC, id DESC。
func (r *Repo) PaymentsByOrder(ctx context.Context, orderID uuid.UUID) ([]PaymentRow, error) {
	var out []PaymentRow
	err := r.d.Conn(ctx).Raw(
		`SELECT p.*, pp.code AS provider_code, pp.display_name AS provider_name
		 FROM payments p
		 JOIN payment_providers pp ON pp.id = p.provider_id
		 WHERE p.order_id = ?
		 ORDER BY p.paid_at DESC, p.id DESC`,
		orderID,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []PaymentRow{}, nil
	}
	return out, nil
}

// SavePoint 在当前事务里开保存点 name（只用常量名，不拼接外部输入）。
func (r *Repo) SavePoint(ctx context.Context, name string) error {
	return r.d.Conn(ctx).Exec("SAVEPOINT " + name).Error
}

// RollbackToSavePoint 回滚到保存点 name：其后的写入撤销，失败过的语句也不再让事务作废。
func (r *Repo) RollbackToSavePoint(ctx context.Context, name string) error {
	return r.d.Conn(ctx).Exec("ROLLBACK TO SAVEPOINT " + name).Error
}

// PurgeUnverifiedEvents 调库函数 purge_unverified_payment_events(before, batch)，返回删除行数。
func (r *Repo) PurgeUnverifiedEvents(ctx context.Context, before time.Time, batch int) (int, error) {
	var n int
	err := r.d.Conn(ctx).Raw("SELECT purge_unverified_payment_events(?, ?)", before, batch).Scan(&n).Error
	return n, err
}
