package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// ProviderAmount 是一个渠道在一个币种上的金额合计。
type ProviderAmount struct {
	ProviderID uuid.UUID
	Currency   string
	Amount     decimal.Decimal
}

// ProviderByCode 按代码读一行（不加锁）。
func (r *Repo) ProviderByCode(ctx context.Context, code string) (model.Provider, bool, error) {
	return r.providerOne(ctx, "code = ?", code, false)
}

// ProviderByID 按 ID 读一行（不加锁）。
func (r *Repo) ProviderByID(ctx context.Context, id uuid.UUID) (model.Provider, bool, error) {
	return r.providerOne(ctx, "id = ?", id, false)
}

// LockProviderByCode 按代码读一行并 FOR UPDATE。
func (r *Repo) LockProviderByCode(ctx context.Context, code string) (model.Provider, bool, error) {
	return r.providerOne(ctx, "code = ?", code, true)
}

func (r *Repo) providerOne(ctx context.Context, where string, arg any, lock bool) (model.Provider, bool, error) {
	q := r.d.Conn(ctx).Where(where, arg).Limit(1)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.Provider
	if err := q.Find(&rows).Error; err != nil {
		return model.Provider{}, false, err
	}
	if len(rows) == 0 {
		return model.Provider{}, false, nil
	}
	return rows[0], true, nil
}

// ListProviders 全部渠道，按 code 升序。
func (r *Repo) ListProviders(ctx context.Context) ([]model.Provider, error) {
	var out []model.Provider
	if err := r.d.Conn(ctx).Order("code").Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.Provider{}, nil
	}
	return out, nil
}

// ListAcceptingProviders 启用且收新单的渠道，按 code 升序。
func (r *Repo) ListAcceptingProviders(ctx context.Context) ([]model.Provider, error) {
	var out []model.Provider
	if err := r.d.Conn(ctx).Where("enabled AND accepting_new").Order("code").Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.Provider{}, nil
	}
	return out, nil
}

// InsertProvider 插一行；p.ID 由调用方先填好（uuid.NewV7）。code 重复返回 ErrDuplicate。写前 money.Check(p.MinAmount)。
func (r *Repo) InsertProvider(ctx context.Context, p *model.Provider) error {
	if err := money.Check(p.MinAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Create(p).Error)
}

// UpdateProviderFields 按 ID 更新给定列（键是列名，如 "display_name"、"credentials_sealed"），顺带 updated_at = now()。返回是否更新到一行。
func (r *Repo) UpdateProviderFields(ctx context.Context, id uuid.UUID, fields map[string]any) (bool, error) {
	if len(fields) == 0 {
		return false, nil
	}
	updates := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		if k == "min_amount" {
			amt, ok := v.(decimal.Decimal)
			if !ok {
				return false, fmt.Errorf("payment: min_amount 类型错误")
			}
			if err := money.Check(amt); err != nil {
				return false, err
			}
			updates[k] = amt
			continue
		}
		if k == "currencies" || k == "methods" {
			arr, ok := v.([]string)
			if !ok {
				return false, fmt.Errorf("payment: %s 须为 []string", k)
			}
			b, err := json.Marshal(arr)
			if err != nil {
				return false, err
			}
			updates[k] = gorm.Expr("?::jsonb", string(b))
			continue
		}
		updates[k] = v
	}
	updates["updated_at"] = gorm.Expr("now()")
	res := r.d.Conn(ctx).Model(&model.Provider{}).Where("id = ?", id).Updates(updates)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// MinAcceptingAmount 启用、收新单、币种含 cur 的渠道里最小的 min_amount；一个都没有时 found=false。
func (r *Repo) MinAcceptingAmount(ctx context.Context, cur string) (decimal.Decimal, bool, error) {
	var min sql.NullString
	err := r.d.Conn(ctx).Raw(
		`SELECT MIN(min_amount) FROM payment_providers
		 WHERE enabled AND accepting_new AND currencies @> jsonb_build_array(?::text)`,
		cur,
	).Scan(&min).Error
	if err != nil {
		return decimal.Zero, false, err
	}
	if !min.Valid {
		return decimal.Zero, false, nil
	}
	amt, err := decimal.NewFromString(min.String)
	if err != nil {
		return decimal.Zero, false, err
	}
	return amt, true, nil
}

// ProviderTodayTotals 各渠道「站点时区今天」成功收款的合计。
func (r *Repo) ProviderTodayTotals(ctx context.Context, tz string) ([]ProviderAmount, error) {
	var out []ProviderAmount
	err := r.d.Conn(ctx).Raw(
		`SELECT provider_id, currency, SUM(amount) AS amount
		 FROM payments
		 WHERE status = 'succeeded'
		   AND (paid_at AT TIME ZONE @tz)::date = (now() AT TIME ZONE @tz)::date
		 GROUP BY provider_id, currency`,
		map[string]any{"tz": tz},
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []ProviderAmount{}, nil
	}
	return out, nil
}

// ProviderSuccessRates 各渠道 since 之后进入终态的意图里 succeeded 的比例；没有样本的渠道不出现在 map 里。
func (r *Repo) ProviderSuccessRates(ctx context.Context, since time.Time) (map[uuid.UUID]float64, error) {
	var rows []struct {
		ProviderID uuid.UUID
		Rate       float64
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT provider_id,
		        COUNT(*) FILTER (WHERE status = 'succeeded')::double precision
		          / COUNT(*)::double precision AS rate
		 FROM payment_intents
		 WHERE status IN ('succeeded', 'cancelled', 'expired')
		   AND updated_at >= ?
		 GROUP BY provider_id
		 HAVING COUNT(*) > 0`,
		since,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]float64, len(rows))
	for _, row := range rows {
		out[row.ProviderID] = row.Rate
	}
	return out, nil
}

// ProviderLastEvents 各渠道最近一条事件的 received_at。
func (r *Repo) ProviderLastEvents(ctx context.Context) (map[uuid.UUID]time.Time, error) {
	var rows []struct {
		ProviderID uuid.UUID
		LastAt     time.Time
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT provider_id, MAX(received_at) AS last_at FROM payment_events GROUP BY provider_id`,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]time.Time, len(rows))
	for _, row := range rows {
		out[row.ProviderID] = row.LastAt
	}
	return out, nil
}
