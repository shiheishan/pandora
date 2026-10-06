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
	"net/http"
	"strings"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type generateCouponsReq struct {
	createCouponReq
	// Count 这一批生成多少张。
	Count int `json:"count"`
	// Prefix 券码前缀，便于一眼认出是哪次活动的券。可以留空。
	Prefix string `json:"prefix"`
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

type generateCouponsResponse struct {
	Codes []string `json:"codes"`
	Count int      `json:"count"`
	Name  string   `json:"name"`
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

	codes, err := h.d.Billing.AdminGenerateCoupons(r.Context(),
		couponSpec(tenantID, actorID, &req.createCouponReq, from, until, perUser),
		req.Prefix, req.Count, maxRedeem)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, generateCouponsResponse{Codes: codes, Count: len(codes), Name: req.Name})
}
