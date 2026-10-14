package repo

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/promo/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// checkCouponMoney 写入前校验券上所有金额字段。
func checkCouponMoney(c *model.Coupon) error {
	var vals []decimal.Decimal
	vals = append(vals, c.MinOrderAmount)
	if c.AmountOff != nil {
		vals = append(vals, *c.AmountOff)
	}
	if c.MaxDiscount != nil {
		vals = append(vals, *c.MaxDiscount)
	}
	return checkMoney(vals...)
}

// InsertCouponIfAbsent 插入一张券及其限定的套餐，券码已存在时跳过（ON CONFLICT DO NOTHING，不让事务出错）；返回是否插入。
func (r *Repo) InsertCouponIfAbsent(ctx context.Context, c *model.Coupon, planIDs []uuid.UUID) (bool, error) {
	if err := checkCouponMoney(c); err != nil {
		return false, err
	}
	res := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "code"}},
		DoNothing: true,
	}).Create(c)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	if res.RowsAffected != 1 {
		return false, nil
	}
	if len(planIDs) > 0 {
		rows := make([]model.CouponPlan, len(planIDs))
		for i, id := range planIDs {
			rows[i] = model.CouponPlan{CouponID: c.ID, PlanID: id}
		}
		if err := r.d.Conn(ctx).Create(&rows).Error; err != nil {
			return false, mapErr(err)
		}
	}
	return true, nil
}

// InsertCouponsIfAbsent 用一条语句插入一批券码相同规格的券（tmpl 提供除 code 外的全部字段）及其限定的套餐：
// 撞已有券码的跳过（ON CONFLICT DO NOTHING，不让事务出错），返回真正插入的券码（顺序不定）。
// 券与 coupon_plans 在同一条语句里写（数据修改 CTE），往返次数与张数无关。
func (r *Repo) InsertCouponsIfAbsent(ctx context.Context, tmpl *model.Coupon, codes []string, planIDs []uuid.UUID) ([]string, error) {
	if len(codes) == 0 {
		return []string{}, nil
	}
	if err := checkCouponMoney(tmpl); err != nil {
		return nil, err
	}

	var amountOff, percentOffBP, maxDiscount, maxRedemptions any
	if tmpl.AmountOff != nil {
		amountOff = *tmpl.AmountOff
	}
	if tmpl.PercentOffBP != nil {
		percentOffBP = *tmpl.PercentOffBP
	}
	if tmpl.MaxDiscount != nil {
		maxDiscount = *tmpl.MaxDiscount
	}
	if tmpl.MaxRedemptions != nil {
		maxRedemptions = *tmpl.MaxRedemptions
	}
	var userGroup, createdBy any
	if tmpl.UserGroupID != nil {
		userGroup = *tmpl.UserGroupID
	}
	if tmpl.CreatedBy != nil {
		createdBy = *tmpl.CreatedBy
	}

	const sql = `
WITH ins AS (
  INSERT INTO coupons (code, name, amount_off, percent_off_bp, currency, max_discount, min_order_amount,
                       max_redemptions, max_redemptions_per_user, user_group_id, valid_from, valid_until, status, created_by)
  SELECT c, CAST(? AS text), CAST(? AS numeric), CAST(? AS integer), CAST(? AS text), CAST(? AS numeric),
         CAST(? AS numeric), CAST(? AS integer), CAST(? AS integer), CAST(? AS uuid),
         CAST(? AS timestamptz), CAST(? AS timestamptz), CAST(? AS text), CAST(? AS uuid)
    FROM unnest(CAST(? AS text[])) AS c
  ON CONFLICT (code) DO NOTHING
  RETURNING id, code
), plans AS (
  INSERT INTO coupon_plans (coupon_id, plan_id)
  SELECT ins.id, p FROM ins CROSS JOIN unnest(CAST(? AS uuid[])) AS p
)
SELECT code FROM ins`

	rows, err := r.d.Conn(ctx).Raw(sql,
		tmpl.Name,
		amountOff,
		percentOffBP,
		tmpl.Currency,
		maxDiscount,
		tmpl.MinOrderAmount,
		maxRedemptions,
		tmpl.MaxRedemptionsPerUser,
		userGroup,
		tmpl.ValidFrom,
		tmpl.ValidUntil,
		tmpl.Status,
		createdBy,
		textArrayLiteral(codes),
		uuidArray(planIDs),
	).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var inserted []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, mapErr(err)
		}
		inserted = append(inserted, code)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return inserted, nil
}

// PlanIDsByCoupons 批量取券限定的套餐（一条 SQL）；没有限定的券不在结果里。
func (r *Repo) PlanIDsByCoupons(ctx context.Context, couponIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	out := make(map[uuid.UUID][]uuid.UUID, len(couponIDs))
	if len(couponIDs) == 0 {
		return out, nil
	}
	// 逐行用 database/sql 扫：GORM 的 Scan 会把 uuid.UUID 当 [16]byte 逐元素扫
	rows, err := r.d.Conn(ctx).Raw(
		"SELECT coupon_id, plan_id FROM coupon_plans WHERE coupon_id IN ? ORDER BY coupon_id, plan_id", couponIDs,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, pid uuid.UUID
		if err := rows.Scan(&cid, &pid); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], pid)
	}
	return out, rows.Err()
}

// CouponByCode 按券码查券。
func (r *Repo) CouponByCode(ctx context.Context, code string) (model.Coupon, bool, error) {
	var c model.Coupon
	err := r.d.Conn(ctx).Where("code = ?", code).Take(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Coupon{}, false, nil
		}
		return model.Coupon{}, false, err
	}
	return c, true, nil
}

// LockCouponByCode 按券码查券并加行锁（SELECT … FOR UPDATE）。
func (r *Repo) LockCouponByCode(ctx context.Context, code string) (model.Coupon, bool, error) {
	var c model.Coupon
	err := r.d.Conn(ctx).Where("code = ?", code).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Take(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Coupon{}, false, nil
		}
		return model.Coupon{}, false, err
	}
	return c, true, nil
}

// SetCouponStatus 更新券状态。
func (r *Repo) SetCouponStatus(ctx context.Context, id uuid.UUID, status string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE coupons SET status = ?, updated_at = now() WHERE id = ?", status, id,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// IncrementReserved 在容量与 active 条件下把 reserved_count 加一。
func (r *Repo) IncrementReserved(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE coupons SET reserved_count = reserved_count + 1, updated_at = now()
		WHERE id = ? AND status = 'active'
		AND (max_redemptions IS NULL OR redeemed_count + reserved_count < max_redemptions)`, id)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// DecrementReserved 在 reserved_count > 0 时减一。
func (r *Repo) DecrementReserved(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE coupons SET reserved_count = reserved_count - 1, updated_at = now() WHERE id = ? AND reserved_count > 0", id,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// MoveReservedToRedeemed 在 reserved_count > 0 时把占用转为已核销计数。
func (r *Repo) MoveReservedToRedeemed(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE coupons SET reserved_count = reserved_count - 1, redeemed_count = redeemed_count + 1, updated_at = now()
		WHERE id = ? AND reserved_count > 0`, id)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// CouponListQuery 是后台券列表筛选：Query 是用户输入的原文（这里转义成 ILIKE 包含模式），非空时匹配券码或名称；Status 非空时按状态。
type CouponListQuery struct {
	Query  string
	Status string
}

// CouponListRow 是列表一行，含未释放占用的折扣总额。
type CouponListRow struct {
	model.Coupon
	DiscountedTotal decimal.Decimal `gorm:"column:discounted_total"`
}

func couponListWhere(q CouponListQuery) (clause string, args []any) {
	var parts []string
	if query := strings.TrimSpace(q.Query); query != "" {
		pat := db.ContainsPattern(query)
		parts = append(parts, "(c.code ILIKE ? ESCAPE '\\' OR c.name ILIKE ? ESCAPE '\\')")
		args = append(args, pat, pat)
	}
	if strings.TrimSpace(q.Status) != "" {
		parts = append(parts, "c.status = ?")
		args = append(args, q.Status)
	}
	if len(parts) == 0 {
		return "true", nil
	}
	return strings.Join(parts, " AND "), args
}

// ListCoupons 分页列出券及每券 discounted_total。
func (r *Repo) ListCoupons(ctx context.Context, q CouponListQuery, limit, offset int) ([]CouponListRow, int64, error) {
	where, args := couponListWhere(q)
	conn := r.d.Conn(ctx)

	var total int64
	countSQL := "SELECT count(*) FROM coupons c WHERE " + where
	if err := conn.Raw(countSQL, args...).Scan(&total).Error; err != nil {
		return nil, 0, err
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, limit, offset)
	listSQL := `SELECT c.*, COALESCE((SELECT sum(rd.discount_amount) FROM coupon_redemptions rd
		WHERE rd.coupon_id = c.id AND rd.status <> 'released'), 0) AS discounted_total
		FROM coupons c WHERE ` + where + ` ORDER BY c.created_at DESC, c.code LIMIT ? OFFSET ?`

	var rows []CouponListRow
	if err := conn.Raw(listSQL, listArgs...).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	if rows == nil {
		return []CouponListRow{}, total, nil
	}
	return rows, total, nil
}

// CouponCodes 批量查券 id 对应的 code。
func (r *Repo) CouponCodes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	type idCode struct {
		ID   uuid.UUID `gorm:"column:id"`
		Code string    `gorm:"column:code"`
	}
	var rows []idCode
	if err := r.d.Conn(ctx).Raw("SELECT id, code FROM coupons WHERE id IN ?", ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		out[row.ID] = row.Code
	}
	return out, nil
}

// textArrayLiteral 把字符串切片编成 PostgreSQL text 数组字面量（元素双引号转义）；
// 写法照抄 identity/repo/generation.go 的 textArrayLiteral。
func textArrayLiteral(ss []string) string {
	if len(ss) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range s {
			if r == '\\' || r == '"' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// uuidArray 把 UUID 切片编成 PostgreSQL uuid 数组字面量；写法照抄 identity/repo/ops_users.go 的 uuidArray。
func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(id.String())
	}
	b.WriteByte('}')
	return b.String()
}
