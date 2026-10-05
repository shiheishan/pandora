// [INPUT]: 依赖 platform 的 db 租户事务与唯一约束判定、audit 同事务审计、httpx 的错误模型与请求 ID；读写 coupons，读 coupon_redemptions / users / orders
// [OUTPUT]: 对外提供 Service.AdminListCoupons / AdminCreateCoupon / AdminGenerateCoupons / AdminSetCouponStatus / AdminCouponRedemptions 与 AdminCoupon、AdminCouponRedemption、AdminCouponSpec
// [POS]: domain/billing 的后台优惠券用例（从 api/admin/coupon.go 与 coupon_batch.go 下沉）：券只停用不删除；批量生券在一个事务里随机出码、撞码由 (tenant_id, code) 唯一约束兜住后重试并封顶；与 coupon.go 的结账核销读同一张表；请求规范化与校验留在 handler
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// AdminCoupon 是后台优惠券列表的一行。
type AdminCoupon struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	DiscountType  string   `json:"discount_type"`
	DiscountValue int64    `json:"discount_value"`
	Currency      string   `json:"currency"`
	MaxDiscount   *int64   `json:"max_discount"`
	MinOrder      int64    `json:"min_order_amount"`
	MaxRedeem     *int     `json:"max_redemptions"`
	MaxPerUser    int      `json:"max_redemptions_per_user"`
	Redeemed      int      `json:"redeemed_count"`
	PlanIDs       []string `json:"applicable_plan_ids"`
	ValidFrom     any      `json:"valid_from"`
	ValidUntil    any      `json:"valid_until"`
	Status        string   `json:"status"`
	CreatedAt     any      `json:"created_at"`
	// Discounted 是这张券实际减掉的总金额。列表里直接给出来，
	// 免得管理员为了判断一张券值不值得续办还要自己去翻核销明细。
	Discounted int64 `json:"discounted_total"`
}

// AdminListCoupons 按券码或名称模糊匹配（like 已带 %）、按状态（LIKE，% 即不筛）分页列券，并回总数。
func (s *Service) AdminListCoupons(ctx context.Context, tenantID, like, status string, limit, offset int) ([]AdminCoupon, int64, error) {
	out := []AdminCoupon{}
	var total int64

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		const cond = `
			 WHERE c.tenant_id = $1
			   AND (lower(c.code::text) LIKE $2 OR lower(c.name) LIKE $2)
			   AND c.status LIKE $3`

		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM coupons c`+cond, tenantID, like, status).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, `
			SELECT c.id::text, c.code::text, c.name, c.discount_type, c.discount_value,
			       COALESCE(c.currency::text,''), c.max_discount, c.min_order_amount,
			       c.max_redemptions, c.max_redemptions_per_user, c.redeemed_count,
			       c.applicable_plan_ids::text[], c.valid_from, c.valid_until,
			       c.status, c.created_at,
			       COALESCE((SELECT sum(rd.discount_amount) FROM coupon_redemptions rd
			                  WHERE rd.coupon_id = c.id AND rd.reverted_at IS NULL), 0)
			  FROM coupons c`+cond+`
			 ORDER BY c.created_at DESC, c.code
			 LIMIT $4 OFFSET $5`, tenantID, like, status, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c AdminCoupon
			if err := rows.Scan(&c.ID, &c.Code, &c.Name, &c.DiscountType, &c.DiscountValue,
				&c.Currency, &c.MaxDiscount, &c.MinOrder, &c.MaxRedeem, &c.MaxPerUser,
				&c.Redeemed, &c.PlanIDs, &c.ValidFrom, &c.ValidUntil, &c.Status,
				&c.CreatedAt, &c.Discounted); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// AdminCouponSpec 是已规范化、已校验的券规格，单张创建与批量生成共用。
type AdminCouponSpec struct {
	TenantID      string
	ActorID       *string
	Name          string
	DiscountType  string
	DiscountValue int64
	Currency      string
	MaxDiscount   *int64
	MinOrder      int64
	PerUser       int
	PlanIDs       []string
	ValidFrom     *time.Time
	ValidUntil    *time.Time
}

// AdminCreateCoupon 新建一张券，返回 id；同事务写 coupon.created 审计，券码重复回 409。
func (s *Service) AdminCreateCoupon(ctx context.Context, in AdminCouponSpec, code string, maxRedeem *int) (string, error) {
	tenantID := in.TenantID
	var newID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO coupons (tenant_id, code, name, discount_type, discount_value,
				currency, max_discount, min_order_amount, max_redemptions,
				max_redemptions_per_user, applicable_plan_ids, valid_from, valid_until,
				status, created_by)
			VALUES ($1,$2,$3,$4,$5,$6::app.currency_code,$7,$8,$9,$10,$11::uuid[],$12,$13,
				'active',$14::uuid)
			RETURNING id::text`,
			tenantID, code, in.Name, in.DiscountType, in.DiscountValue,
			in.Currency, in.MaxDiscount, in.MinOrder, maxRedeem,
			in.PerUser, in.PlanIDs, in.ValidFrom, in.ValidUntil, in.ActorID).Scan(&newID)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: in.ActorID,
			Action: "coupon.created", ResourceType: "coupon", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"code": code, "type": in.DiscountType, "value": in.DiscountValue},
		})
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			return "", httpx.New(httpx.CodeConflict, "这个优惠码已经存在")
		}
		return "", err
	}
	return newID, nil
}

// couponCodeAlphabet 去掉了 I、L、O、0、1。
//
// 券码要印在物料上、贴进群公告、被用户手工敲进结账框。这几个字符在
// 多数字体里几乎不可分辨，认错一次就是一张废券加一个工单。和礼品卡
// 卡密用的是同一套字母表，理由相同。
const couponCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newCouponCode 生成 10 位随机码。
//
// 31^10 ≈ 8.2e14，比礼品卡的 12 位短一些 —— 券码要人手输入，短两位
// 是实打实的体验差别，而券本身有额度和有效期兜底，不像卡密那样等价
// 于现金。撞码由数据库唯一约束兜住，撞了就换一个。
func newCouponCode(prefix string) (string, error) {
	const n = 10
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = couponCodeAlphabet[int(v)%len(couponCodeAlphabet)]
	}
	return prefix + string(out), nil
}

// AdminGenerateCoupons 一次生成 count 张配置相同、券码不同的券，返回生成的券码；
// 同事务写一条只记规格与数量的 coupon.batch_generated 审计。
func (s *Service) AdminGenerateCoupons(ctx context.Context, in AdminCouponSpec, prefix string, count, maxRedeem int) ([]string, error) {
	tenantID := in.TenantID
	codes := make([]string, 0, count)
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// attempts 给撞码重试封顶。31^10 的空间里撞码几乎不可能，但如果
		// 真的进入了死循环（比如前缀被人塞成把空间压到很小的东西），
		// 宁可报错也不能让一个事务无限持有锁。
		attempts := 0
		for len(codes) < count {
			attempts++
			if attempts > count*10+100 {
				return httpx.New(httpx.CodeInternal, "生成券码时反复撞码，已中止；请换个前缀重试")
			}
			code, err := newCouponCode(prefix)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO coupons (tenant_id, code, name, discount_type, discount_value,
					currency, max_discount, min_order_amount, max_redemptions,
					max_redemptions_per_user, applicable_plan_ids, valid_from, valid_until,
					status, created_by)
				VALUES ($1,$2,$3,$4,$5,$6::app.currency_code,$7,$8,$9,$10,$11::uuid[],$12,$13,
					'active',$14::uuid)
				ON CONFLICT (tenant_id, code) DO NOTHING`,
				tenantID, code, in.Name, in.DiscountType, in.DiscountValue,
				in.Currency, in.MaxDiscount, in.MinOrder, maxRedeem,
				in.PerUser, in.PlanIDs, in.ValidFrom, in.ValidUntil, in.ActorID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				codes = append(codes, code)
			}
		}

		// 审计只记这一批的规格和数量，不把 1000 个券码塞进 digest ——
		// 券码在 coupons 表里查得到，审计条目本身要保持可读。
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: in.ActorID,
			Action: "coupon.batch_generated", ResourceType: "coupon",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"name": in.Name, "prefix": prefix, "count": len(codes),
				"type": in.DiscountType, "value": in.DiscountValue,
				"max_redemptions": maxRedeem,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// AdminSetCouponStatus 启用或停用一张券（status 为 active / paused，由调用方校验）；同事务写审计。
func (s *Service) AdminSetCouponStatus(ctx context.Context, tenantID string, actorID *string, id, status string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE coupons SET status = $3, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "coupon.status_change", ResourceType: "coupon", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"status": status},
		})
	})
	return err
}

// AdminCouponRedemption 是单张券的一条核销记录。
type AdminCouponRedemption struct {
	Email    string `json:"email"`
	OrderNo  string `json:"order_no"`
	Discount int64  `json:"discount"`
	Currency string `json:"currency"`
	At       any    `json:"at"`
	Reverted bool   `json:"reverted"`
}

// AdminCouponRedemptions 列单张券最近 200 条核销明细。
func (s *Service) AdminCouponRedemptions(ctx context.Context, tenantID, id string) ([]AdminCouponRedemption, error) {
	out := []AdminCouponRedemption{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT COALESCE(u.email::text,''), COALESCE(o.order_no,''),
			       rd.discount_amount, COALESCE(rd.currency::text,''),
			       rd.redeemed_at, rd.reverted_at IS NOT NULL
			  FROM coupon_redemptions rd
			  LEFT JOIN users u ON u.id = rd.user_id
			  LEFT JOIN orders o ON o.id = rd.order_id
			 WHERE rd.tenant_id = $1 AND rd.coupon_id = $2::uuid
			 ORDER BY rd.redeemed_at DESC LIMIT 200`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it AdminCouponRedemption
			if err := rows.Scan(&it.Email, &it.OrderNo, &it.Discount, &it.Currency,
				&it.At, &it.Reverted); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
