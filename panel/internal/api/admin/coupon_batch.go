package admin

// 批量生成优惠券。
//
// 单张创建对付不了活动：给渠道发 200 张一次性券、双十一发 500 张，
// 一张一张填表填到手断，而且每张都要现编一个不重复的码。xboard 的
// 后台有这个（Coupon/generate），是券功能真正能用起来的前提。
//
// 生成出来的券共用一份折扣配置和同一个名称（活动名），只有券码不同。
// 名称就是这一批的抓手：列表里按名称搜就能把整批捞出来 —— 表里没有
// batch_id 字段，为这个加一次迁移不划算，而共用名称本来也是活动券的
// 自然形态。
//
// 默认每张只能用一次（max_redemptions = 1）。批量券的语义就是「一人
// 一张」，如果每张都能无限用，那生成 500 张和生成 1 张没有区别 ——
// 这个默认值弄反了是会直接亏钱的，所以不跟随单张创建的「不限」默认。

import (
	"crypto/rand"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type generateCouponsReq struct {
	createCouponReq
	// Count 这一批生成多少张。
	Count int `json:"count"`
	// Prefix 券码前缀，便于一眼认出是哪次活动的券。可以留空。
	Prefix string `json:"prefix"`
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

func isSafeCouponPrefix(p string) bool {
	if len(p) > 8 {
		return false
	}
	for _, c := range p {
		if !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// generateCoupons 一次生成多张配置相同、券码不同的优惠券。
func (h *handlers) generateCoupons(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req generateCouponsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	req.Prefix = strings.ToUpper(strings.TrimSpace(req.Prefix))
	if req.Count < 1 || req.Count > 1000 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"count": "一次生成 1 到 1000 张。要更多就分批 —— 一次几万张会把事务拖很久，" +
				"而且生成出来的码也没法在一个页面里交接"}))
		return
	}
	if req.Prefix != "" && !isSafeCouponPrefix(req.Prefix) {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"prefix": "前缀只能用大写字母和数字，最多 8 位"}))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		// 名称是这一批唯一的抓手，不能省。券码是随机的，没有名称的话
		// 事后没有任何办法把这 500 张认回来是哪次活动发的。
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"name": "批量生成必须填活动名称，之后要靠它把整批券找回来"}))
		return
	}

	// needCode = false：券码是这里现场生成的，请求里没有。
	from, until, perUser, err := normalizeCouponReq(&req.createCouponReq, false)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	// 每张券默认只能核销一次。调用方显式给了更大的值才放行。
	maxRedeem := 1
	if req.MaxRedeem != nil && *req.MaxRedeem > 0 {
		maxRedeem = *req.MaxRedeem
	}

	actor := httpx.PrincipalFrom(r.Context())
	var actorID *string
	if actor != nil && actor.UserID != "" {
		id := actor.UserID
		actorID = &id
	}

	codes := make([]string, 0, req.Count)
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// attempts 给撞码重试封顶。31^10 的空间里撞码几乎不可能，但如果
		// 真的进入了死循环（比如前缀被人塞成把空间压到很小的东西），
		// 宁可报错也不能让一个事务无限持有锁。
		attempts := 0
		for len(codes) < req.Count {
			attempts++
			if attempts > req.Count*10+100 {
				return httpx.New(httpx.CodeInternal, "生成券码时反复撞码，已中止；请换个前缀重试")
			}
			code, err := newCouponCode(req.Prefix)
			if err != nil {
				return err
			}
			tag, err := tx.Exec(r.Context(), `
				INSERT INTO coupons (tenant_id, code, name, discount_type, discount_value,
					currency, max_discount, min_order_amount, max_redemptions,
					max_redemptions_per_user, applicable_plan_ids, valid_from, valid_until,
					status, created_by)
				VALUES ($1,$2,$3,$4,$5,$6::app.currency_code,$7,$8,$9,$10,$11::uuid[],$12,$13,
					'active',$14::uuid)
				ON CONFLICT (tenant_id, code) DO NOTHING`,
				tenantID, code, req.Name, req.DiscountType, req.DiscountValue,
				req.Currency, req.MaxDiscount, req.MinOrder, maxRedeem,
				perUser, req.PlanIDs, from, until, actorID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				codes = append(codes, code)
			}
		}

		// 审计只记这一批的规格和数量，不把 1000 个券码塞进 digest ——
		// 券码在 coupons 表里查得到，审计条目本身要保持可读。
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "coupon.batch_generated", ResourceType: "coupon",
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{
				"name": req.Name, "prefix": req.Prefix, "count": len(codes),
				"type": req.DiscountType, "value": req.DiscountValue,
				"max_redemptions": maxRedeem,
			},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, map[string]any{"count": len(codes), "codes": codes, "name": req.Name})
}
