// [INPUT]: 依赖 coupons / coupon_redemptions / prices / plans / traffic_packs 表，依赖 platform/db、platform/httpx
// [OUTPUT]: 对外提供 PreviewForPrice、PreviewForTrafficPack（试算，响应带券面 coupon）；包内提供 applyCoupon / redeemCoupon 与 couponMatch
// [POS]: billing 的优惠券校验与核销：下单、续费、变更套餐、流量包与两种试算共用 applyCoupon 这一个口径
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

// 优惠券。
//
// 校验与核销分两步，而且都在下单那个序列化事务里完成：
//
//	applyCoupon  校验 + 算折扣（决定这一单能减多少）
//	redeemCoupon 记核销 + 累加已用次数（把名额真正扣掉）
//
// 两步必须同事务：中间任何间隙都会让「限量 100 张」在并发下超发。
// 下单本来就用了 InTxSerializable（库存与限购也靠它），这里搭顺风车。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 优惠码相关的失败都是「用户输入有问题」，不是系统故障。
//
// 用 httpx.New 而不是 errors.New：后者会被 httpx.Fail 当成未知错误，
// 一律回 500「服务暂时不可用」—— 用户看到的是系统坏了，
// 而实际上只是他的券过期了。这类错误必须自带 HTTP 语义。
var (
	ErrCouponNotFound   = httpx.New(httpx.CodeValidationFailed, "优惠码不存在或已失效")
	ErrCouponExpired    = httpx.New(httpx.CodeValidationFailed, "优惠码已过期")
	ErrCouponUsedUp     = httpx.New(httpx.CodeConflict, "优惠码已达使用上限")
	ErrCouponUserLimit  = httpx.New(httpx.CodeConflict, "你已使用过这个优惠码")
	ErrCouponMinAmount  = httpx.New(httpx.CodeValidationFailed, "订单金额未达到优惠码的使用门槛")
	ErrCouponNotForPlan = httpx.New(httpx.CodeValidationFailed, "这个优惠码不适用于所选套餐")
	// 不说破「你不在某个组里」—— 那等于告诉人存在一个内部分组，
	// 只需要让他知道这码他用不了
	ErrCouponNotForUser = httpx.New(httpx.CodeValidationFailed, "这个优惠码不可用")
)

// couponMatch 是一次成功校验的结果。
type couponMatch struct {
	ID       string
	Discount int64 // 最小货币单位
	// 试算回显「已使用 CODE：20% 折扣 / 立减 ¥X」要的券面
	Code          string
	DiscountType  string // percent | fixed
	DiscountValue int64  // percent 为万分比，fixed 为分
}

// applyCoupon 校验优惠码并算出这一单能减多少。
//
// 返回的折扣已经按订单金额封顶：折扣超过订单金额时只减到 0，
// 不产生负数。负数会一路穿到支付金额和账本上，那种错误很难往回追。
func applyCoupon(ctx context.Context, tx pgx.Tx, tenantID, userID, code string,
	planID, currency string, subtotal int64) (*couponMatch, error) {

	code = strings.TrimSpace(strings.ToUpper(code))
	if code == "" {
		return nil, nil
	}

	var (
		id            string
		discountType  string
		discountValue int64
		couponCur     *string
		maxDiscount   *int64
		minOrder      *int64
		planIDs       []string
		maxRedeem     *int
		maxPerUser    *int
		redeemed      int
		reserved      int
		validFrom     *time.Time
		validUntil    *time.Time
		status        string
		groupIDs      []string
	)

	// 码统一按大写存与比：用户从聊天记录里复制过来常常带着大小写差异，
	// 为此让他们反复试错没有意义。
	err := tx.QueryRow(ctx, `
		SELECT id::text, discount_type, discount_value, currency, max_discount,
		       min_order_amount, COALESCE(applicable_plan_ids, ARRAY[]::uuid[])::text[],
		       max_redemptions, max_redemptions_per_user, redeemed_count, reserved_count,
		       valid_from, valid_until, status,
		       COALESCE(applicable_user_group_ids, ARRAY[]::uuid[])::text[]
		  FROM coupons
		 WHERE tenant_id = $1 AND upper(code) = $2
		 FOR UPDATE`, tenantID, code).Scan(
		&id, &discountType, &discountValue, &couponCur, &maxDiscount,
		&minOrder, &planIDs, &maxRedeem, &maxPerUser, &redeemed, &reserved,
		&validFrom, &validUntil, &status, &groupIDs)
	if err == pgx.ErrNoRows {
		return nil, ErrCouponNotFound
	}
	if err != nil {
		return nil, err
	}

	if status != "active" {
		return nil, ErrCouponNotFound
	}
	now := time.Now()
	if validFrom != nil && now.Before(*validFrom) {
		// 还没开始的券与不存在的券给同一个错误：提前告诉用户
		// 「这张券明天生效」，等于把未公布的活动泄露出去
		return nil, ErrCouponNotFound
	}
	if validUntil != nil && now.After(*validUntil) {
		return nil, ErrCouponExpired
	}
	if maxRedeem != nil && redeemed+reserved >= *maxRedeem {
		return nil, ErrCouponUsedUp
	}
	if couponCur != nil && *couponCur != "" && *couponCur != currency {
		return nil, ErrCouponNotFound
	}
	if minOrder != nil && subtotal < *minOrder {
		return nil, ErrCouponMinAmount
	}
	// 限定用户组：券只对特定人群有效（老用户回馈、代理专享）。
	// 这个判断放在套餐判断之前 —— 组不对的人根本不该看到任何折扣，
	// 提示他「换个套餐试试」是误导
	if len(groupIDs) > 0 {
		var inGroup bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM users
			                WHERE tenant_id = $1 AND id = $2::uuid
			                  AND user_group_id::text = ANY($3))`,
			tenantID, userID, groupIDs).Scan(&inGroup); err != nil {
			return nil, err
		}
		if !inGroup {
			return nil, ErrCouponNotForUser
		}
	}

	if len(planIDs) > 0 {
		ok := false
		for _, p := range planIDs {
			if p == planID {
				ok = true
				break
			}
		}
		if !ok {
			return nil, ErrCouponNotForPlan
		}
	}

	if maxPerUser != nil && *maxPerUser > 0 {
		var used int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM coupon_redemptions
			 WHERE tenant_id = $1 AND coupon_id = $2::uuid AND user_id = $3::uuid
			   AND status IN ('held','captured')`, tenantID, id, userID).Scan(&used); err != nil {
			return nil, err
		}
		if used >= *maxPerUser {
			return nil, ErrCouponUserLimit
		}
	}

	var discount int64
	switch discountType {
	case "percent":
		// discount_value 存的是万分比（basis point）：2000 表示 8 折。
		// 不用百分比是因为 12.5% 这种折扣在整数百分比里没法表达，
		// 而 schema 里其它比例字段（cpu_bp 等）也是这个惯例 —— 同一张表
		// 里两种进制共存，迟早有人读错 100 倍。
		//
		// 先乘后除，顺序反了会因为整数除法把小额订单的折扣直接抹成 0。
		// 用 MulDiv 防 int64 乘法溢出（大额订单乘万分比可能超 2^63）。
		var err error
		discount, err = payment.MulDiv(subtotal, discountValue, 10000)
		if err != nil {
			return nil, err
		}
		if maxDiscount != nil && *maxDiscount > 0 && discount > *maxDiscount {
			discount = *maxDiscount
		}
	case "fixed":
		discount = discountValue
	default:
		return nil, ErrCouponNotFound
	}

	if discount > subtotal {
		discount = subtotal
	}
	if discount < 0 {
		discount = 0
	}
	return &couponMatch{ID: id, Discount: discount,
		Code: code, DiscountType: discountType, DiscountValue: discountValue}, nil
}

// redeemCoupon 记一次核销并把名额扣掉。
func redeemCoupon(ctx context.Context, tx pgx.Tx, tenantID, userID, orderID string,
	m *couponMatch, currency string, reservationIDs ...string) error {
	if m == nil {
		return nil
	}
	if len(reservationIDs) != 1 || reservationIDs[0] == "" {
		return errors.New("coupon reservation id is required")
	}
	reservationID := reservationIDs[0]
	if _, err := tx.Exec(ctx, `
		INSERT INTO coupon_redemptions
			(tenant_id, coupon_id, user_id, order_id, discount_amount, currency,
			 reservation_id)
		VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7::uuid)`,
		tenantID, m.ID, userID, orderID, m.Discount, currency, reservationID); err != nil {
		return err
	}
	// 与核销记录同事务累加，两者不会各自为政。
	// 单靠 count(redemptions) 算已用数在高并发下要扫全表，
	// 单靠计数器则会因为撤销而失真 —— 所以两份都留着，
	// 计数器给限量判断用，明细给对账用。
	_, err := tx.Exec(ctx, `
		UPDATE coupons SET reserved_count = reserved_count + 1, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, m.ID)
	return err
}

// PreviewCoupon 供下单前试算，不产生任何写入。
func (s *Service) PreviewCoupon(ctx context.Context, tenantID, userID, code,
	planID, currency string, subtotal int64) (int64, error) {

	var discount int64
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		m, err := applyCoupon(ctx, tx, tenantID, userID, code, planID, currency, subtotal)
		if err != nil {
			return err
		}
		if m != nil {
			discount = m.Discount
		}
		return nil
	})
	return discount, err
}

// PreviewForPrice 按套餐与价格试算优惠码，供下单前的界面显示。
//
// 金额由服务端按 price_id 查出来，不接受前端传值：
// 试算虽然不落库，但显示出来的「立减 X 元」如果能被伪造，
// 用户会带着错误预期走到支付页 —— 那时候的落差是客服工单。
func (s *Service) PreviewForPrice(ctx context.Context, tenantID, userID, code,
	planID, priceID string) (map[string]any, error) {

	return s.previewCoupon(ctx, tenantID, userID, code, planID, func(tx pgx.Tx) (string, int64, error) {
		var currency string
		var subtotal int64
		// 价格必须属于这个套餐（同一个 product）：否则用户可以拿
		// 便宜套餐的价格去试算只对贵套餐生效的券
		err := tx.QueryRow(ctx, `
			SELECT pr.currency, pr.unit_amount
			  FROM prices pr
			  JOIN plans pl ON pl.product_id = pr.product_id AND pl.tenant_id = pr.tenant_id
			 WHERE pr.tenant_id = $1 AND pr.id = $2 AND pl.id = $3 AND pr.status = 'active'`,
			tenantID, priceID, planID).Scan(&currency, &subtotal)
		return currency, subtotal, err
	})
}

// PreviewForTrafficPack 按流量包试算优惠码（结账页流量包模式）。口径与
// CreateTrafficPackOrder 一致：planID 传空，限定套餐的券按「不在适用范围」拒绝。
func (s *Service) PreviewForTrafficPack(ctx context.Context, tenantID, userID, code,
	packID string) (map[string]any, error) {

	return s.previewCoupon(ctx, tenantID, userID, code, "", func(tx pgx.Tx) (string, int64, error) {
		var currency string
		var subtotal int64
		err := tx.QueryRow(ctx, `
			SELECT currency::text, unit_amount FROM traffic_packs
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'active'`,
			tenantID, packID).Scan(&currency, &subtotal)
		return currency, subtotal, err
	})
}

// previewCoupon 是两种试算共用的外壳：price 查出币种与原价（查不到即 404），
// 再按下单同一个 applyCoupon 算折扣。
func (s *Service) previewCoupon(ctx context.Context, tenantID, userID, code, planID string,
	price func(pgx.Tx) (string, int64, error)) (map[string]any, error) {

	out := map[string]any{}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		currency, subtotal, err := price(tx)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		m, err := applyCoupon(ctx, tx, tenantID, userID, code, planID, currency, subtotal)
		if err != nil {
			return err
		}
		var discount int64
		var coupon any // 没用码时为 null
		if m != nil {
			discount = m.Discount
			coupon = map[string]any{
				"code": m.Code, "discount_type": m.DiscountType, "discount_value": m.DiscountValue,
			}
		}
		out = map[string]any{
			"subtotal": subtotal, "discount": discount,
			"payable": subtotal - discount, "currency": currency, "coupon": coupon,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// dbScope 与 checkout 里的写法保持一致。
func dbScope(tenantID, userID string) db.Scope {
	return db.Scope{TenantID: tenantID, ActorID: userID}
}
