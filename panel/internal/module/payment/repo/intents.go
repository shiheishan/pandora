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

// QueryRef 是一次要查的「渠道 + 商户单号」。
type QueryRef struct {
	ProviderID   uuid.UUID
	ProviderCode string
	OutTradeNo   string
	OrderNo      string
}

// DueIntent 是巡检认领到的一条意图。
type DueIntent struct {
	IntentID  uuid.UUID
	OrderID   uuid.UUID
	Attempts  int
	ExpiresAt *time.Time
	Now       time.Time
}

// IntentRow 是带渠道代码与名称的意图。
type IntentRow struct {
	model.Intent
	ProviderCode string
	ProviderName string
}

// OrderSummaryRow 是订单列表上的支付摘要。
type OrderSummaryRow struct {
	OrderID      uuid.UUID
	HasIntent    bool
	ProviderCode *string
	ProviderName *string
}

// LockActiveIntent 读订单的在途意图（status='requires_action'）并 FOR NO KEY UPDATE；没有时 found=false。
// 不用 FOR UPDATE：回调插收款时外键检查会对意图行加 KEY SHARE，FOR UPDATE 与它冲突，
// 会和「订单锁 → 去支付」的事务互相等成死锁；NO KEY UPDATE 与 KEY SHARE 相容。
func (r *Repo) LockActiveIntent(ctx context.Context, orderID uuid.UUID) (model.Intent, bool, error) {
	var rows []model.Intent
	if err := r.d.Conn(ctx).
		Where("order_id = ? AND status = 'requires_action'", orderID).
		Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).
		Limit(1).Find(&rows).Error; err != nil {
		return model.Intent{}, false, err
	}
	if len(rows) == 0 {
		return model.Intent{}, false, nil
	}
	return rows[0], true, nil
}

// CountIntents 订单名下意图总数（任何状态）。
func (r *Repo) CountIntents(ctx context.Context, orderID uuid.UUID) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.Intent{}).Where("order_id = ?", orderID).Count(&n).Error
	return n, err
}

// InsertIntent 插一行；(provider_id, out_trade_no) 或「每单一个在途」冲突返回 ErrDuplicate。写前 money.Check(it.Amount)、money.Check(it.SurchargeAmount)。
// created_at、updated_at 不由应用写，用库的 now()：「建单中」窗口（IntentCreatedWithin）与巡检排程都按库时钟比，多实例时钟偏差不影响。
func (r *Repo) InsertIntent(ctx context.Context, it *model.Intent) error {
	if err := money.Check(it.Amount); err != nil {
		return err
	}
	if err := money.Check(it.SurchargeAmount); err != nil {
		return err
	}
	return mapErr(r.d.Conn(ctx).Omit("CreatedAt", "UpdatedAt").Create(it).Error)
}

// IntentCreatedWithin 报告意图是否在库时钟的最近 window 内创建（created_at > now() - window）。
func (r *Repo) IntentCreatedWithin(ctx context.Context, id uuid.UUID, window time.Duration) (bool, error) {
	// 意图不存在时没有行，按不在窗口内算
	var fresh []bool
	if err := r.d.Conn(ctx).Raw(
		`SELECT created_at > now() - make_interval(secs => ?) FROM payment_intents WHERE id = ?`,
		window.Seconds(), id,
	).Scan(&fresh).Error; err != nil {
		return false, mapErr(err)
	}
	return len(fresh) == 1 && fresh[0], nil
}

// IntentByOutTradeNo 按渠道与商户单号读意图（不加锁）。
func (r *Repo) IntentByOutTradeNo(ctx context.Context, providerID uuid.UUID, outTradeNo string) (model.Intent, bool, error) {
	var rows []model.Intent
	if err := r.d.Conn(ctx).Where("provider_id = ? AND out_trade_no = ?", providerID, outTradeNo).Limit(1).Find(&rows).Error; err != nil {
		return model.Intent{}, false, err
	}
	if len(rows) == 0 {
		return model.Intent{}, false, nil
	}
	return rows[0], true, nil
}

// SetIntentStatus 单个意图 from → to（WHERE id AND status = from），返回是否更新到一行。
func (r *Repo) SetIntentStatus(ctx context.Context, id uuid.UUID, from, to string) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Intent{}).Where("id = ? AND status = ?", id, from).Updates(map[string]any{
		"status":     to,
		"updated_at": gorm.Expr("now()"),
	})
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// IntentFill 是渠道建单成功后回写到占位意图的收银台信息。
type IntentFill struct {
	HTTPMethod  string
	RedirectURL string
	FormFields  map[string]string
	ProviderRef *string
}

// FillPlaceholderIntent 把占位意图（requires_action 且 redirect_url 为空）补上收银台信息；返回是否更新到一行（0 行 = 占位已被作废）。
func (r *Repo) FillPlaceholderIntent(ctx context.Context, id uuid.UUID, f IntentFill) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Intent{}).
		Where("id = ? AND status = 'requires_action' AND redirect_url = ''", id).
		Select("http_method", "redirect_url", "form_fields", "provider_ref", "updated_at").
		Updates(&model.Intent{
			HTTPMethod:  f.HTTPMethod,
			RedirectURL: f.RedirectURL,
			FormFields:  f.FormFields,
			ProviderRef: f.ProviderRef,
			UpdatedAt:   time.Now(),
		})
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// SetIntentProviderRef 给还没有 provider_ref 的意图记上渠道发票号，不看状态（回写输给并发、占位已作废时也记，迟到付款仍能按发票号核对）；返回是否更新到一行。
func (r *Repo) SetIntentProviderRef(ctx context.Context, id uuid.UUID, ref string) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Intent{}).
		Where("id = ? AND provider_ref IS NULL", id).
		Updates(map[string]any{
			"provider_ref": ref,
			"updated_at":   gorm.Expr("now()"),
		})
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// CancelPlaceholderIntent 作废还没补上收银台信息的占位意图（渠道建单失败时）；返回是否更新到一行。
func (r *Repo) CancelPlaceholderIntent(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Model(&model.Intent{}).
		Where("id = ? AND status = 'requires_action' AND redirect_url = ''", id).
		Updates(map[string]any{
			"status":     "cancelled",
			"updated_at": gorm.Expr("now()"),
		})
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// CloseOrderIntents 订单名下所有 status = from 的意图改成 to；except 非 nil 时跳过那一个。返回改了几行。
func (r *Repo) CloseOrderIntents(ctx context.Context, orderID uuid.UUID, except *uuid.UUID, from, to string) (int64, error) {
	q := r.d.Conn(ctx).Model(&model.Intent{}).Where("order_id = ? AND status = ?", orderID, from)
	if except != nil {
		q = q.Where("id <> ?", *except)
	}
	res := q.Updates(map[string]any{
		"status":     to,
		"updated_at": gorm.Expr("now()"),
	})
	if err := mapErr(res.Error); err != nil {
		return 0, err
	}
	return res.RowsAffected, nil
}

// QueryRefs 订单发起过的每一笔支付（任何状态），按 created_at 倒序、id 倒序；JOIN payment_providers 取 code。
func (r *Repo) QueryRefs(ctx context.Context, orderID uuid.UUID) ([]QueryRef, error) {
	var out []QueryRef
	err := r.d.Conn(ctx).Raw(
		`SELECT pi.provider_id, pp.code AS provider_code, pi.out_trade_no, pi.order_no
		 FROM payment_intents pi
		 JOIN payment_providers pp ON pp.id = pi.provider_id
		 WHERE pi.order_id = ?
		 ORDER BY pi.created_at DESC, pi.id DESC`,
		orderID,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []QueryRef{}, nil
	}
	return out, nil
}

// ClaimDueIntent 认领一条到期的在途意图（只锁意图行）。
func (r *Repo) ClaimDueIntent(ctx context.Context, maxAttempts int, firstDelay time.Duration) (DueIntent, bool, error) {
	delaySec := firstDelay.Seconds()
	var rows []DueIntent
	err := r.d.Conn(ctx).Raw(
		`SELECT pi.id AS intent_id, pi.order_id, pi.query_attempts AS attempts, pi.expires_at, now() AS now
		   FROM payment_intents pi
		   JOIN payment_providers pp ON pp.id = pi.provider_id
		  WHERE pi.status = 'requires_action'
		    AND (pi.expires_at IS NULL OR pi.expires_at > now())
		    AND pp.enabled
		    AND pi.query_attempts < @max
		    AND coalesce(pi.next_query_at, pi.created_at + make_interval(secs => @delay)) <= now()
		  ORDER BY coalesce(pi.next_query_at, pi.created_at + make_interval(secs => @delay)), pi.id
		  LIMIT 1 FOR UPDATE OF pi SKIP LOCKED`,
		map[string]any{"max": maxAttempts, "delay": delaySec},
	).Scan(&rows).Error
	if err != nil {
		return DueIntent{}, false, err
	}
	if len(rows) == 0 {
		return DueIntent{}, false, nil
	}
	return rows[0], true, nil
}

// MarkIntentQueried query_attempts + 1、next_query_at = next；返回是否更新到一行。
func (r *Repo) MarkIntentQueried(ctx context.Context, id uuid.UUID, next time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE payment_intents SET query_attempts = query_attempts + 1, next_query_at = ?, updated_at = now() WHERE id = ?`,
		next, id,
	)
	if err := mapErr(res.Error); err != nil {
		return false, err
	}
	return res.RowsAffected == 1, nil
}

// StopIntentQueries query_attempts = greatest(query_attempts, max)：渠道不支持查单时不再排。
func (r *Repo) StopIntentQueries(ctx context.Context, id uuid.UUID, max int) error {
	res := r.d.Conn(ctx).Exec(
		`UPDATE payment_intents SET query_attempts = greatest(query_attempts, ?), updated_at = now() WHERE id = ?`,
		max, id,
	)
	return mapErr(res.Error)
}

// IntentsByOrder 订单的全部意图，JOIN 渠道，按 created_at DESC, id DESC。
func (r *Repo) IntentsByOrder(ctx context.Context, orderID uuid.UUID) ([]IntentRow, error) {
	var out []IntentRow
	err := r.d.Conn(ctx).Raw(
		`SELECT pi.*, pp.code AS provider_code, pp.display_name AS provider_name
		 FROM payment_intents pi
		 JOIN payment_providers pp ON pp.id = pi.provider_id
		 WHERE pi.order_id = ?
		 ORDER BY pi.created_at DESC, pi.id DESC`,
		orderID,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []IntentRow{}, nil
	}
	return out, nil
}

// OrderSummaries 一条 SQL 取一批订单的摘要：HasIntent = 有任何意图；渠道取最近一笔收款的渠道，没有收款取最近一次意图的渠道。
// 只返回至少有一个意图或一笔收款的订单。
func (r *Repo) OrderSummaries(ctx context.Context, orderIDs []uuid.UUID) ([]OrderSummaryRow, error) {
	if len(orderIDs) == 0 {
		return []OrderSummaryRow{}, nil
	}
	var out []OrderSummaryRow
	err := r.d.Conn(ctx).Raw(
		`WITH ids AS (
		  SELECT order_id FROM payment_intents WHERE order_id IN @ids
		  UNION
		  SELECT order_id FROM payments WHERE order_id IN @ids
		)
		SELECT o.order_id,
		       EXISTS (SELECT 1 FROM payment_intents pi WHERE pi.order_id = o.order_id) AS has_intent,
		       COALESCE(pay_pp.code, intent_pp.code) AS provider_code,
		       COALESCE(pay_pp.display_name, intent_pp.display_name) AS provider_name
		FROM ids o
		LEFT JOIN LATERAL (
		  SELECT p.provider_id FROM payments p WHERE p.order_id = o.order_id ORDER BY p.paid_at DESC, p.id DESC LIMIT 1
		) latest_pay ON true
		LEFT JOIN payment_providers pay_pp ON pay_pp.id = latest_pay.provider_id
		LEFT JOIN LATERAL (
		  SELECT pi.provider_id FROM payment_intents pi WHERE pi.order_id = o.order_id ORDER BY pi.created_at DESC, pi.id DESC LIMIT 1
		) latest_intent ON true
		LEFT JOIN payment_providers intent_pp ON intent_pp.id = latest_intent.provider_id`,
		map[string]any{"ids": orderIDs},
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []OrderSummaryRow{}, nil
	}
	return out, nil
}
