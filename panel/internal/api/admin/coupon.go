// [INPUT]: 依赖 domain/billing 的 AdminListCoupons / AdminCreateCoupon / AdminSetCouponStatus / AdminCouponRedemptions 与 AdminCouponSpec（读写与审计在 billing/coupon_admin.go），依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供优惠券列表、新建、启停与兑换记录处理器，以及与批量生成共用的 normalizeCouponReq / couponSpec；成功响应为具名 DTO（*Response）
// [POS]: api/admin 后台-06 优惠券的 HTTP 外壳：规范化与校验请求、调 billing、写响应；券只停用不删除；路径 id 非 UUID 一律中性 404；批量生成在 coupon_batch.go

package admin

// 优惠券管理。
//
// 券只允许停用，不允许删除：已经核销过的券被删掉之后，
// coupon_redemptions 和订单上的 coupon_id 就指向一条不存在的记录，
// 对账时那笔折扣从哪来的永远说不清。停用不影响历史。

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listCouponsResponse struct {
	Coupons []billing.AdminCoupon `json:"coupons"`
	Total   int64                 `json:"total"`
}

// listCoupons 支持按券码/名称搜索、按状态筛选，并分页。
//
// 原来是无条件 `LIMIT 200`，既不告诉调用方总数，也不说自己截断了。
// 平时只有几张券看不出问题，批量生成一次几百张之后，第 201 张往后
// 就凭空消失 —— 界面上没有任何迹象，看起来就像那批券没生成成功。
func (h *handlers) listCoupons(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	q := r.URL.Query()

	// 搜索同时匹配券码和名称：批量生成的券共用一个名称（活动名），
	// 按名称搜就能把一整批捞出来。
	like := "%" + strings.ToLower(strings.TrimSpace(q.Get("q"))) + "%"
	status := q.Get("status")
	if status == "" {
		status = "%"
	}
	limit := atoiDefault(q.Get("limit"), 25)
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	offset := atoiDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}

	out, total, err := h.d.Billing.AdminListCoupons(r.Context(), tenantID, like, status, limit, offset)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listCouponsResponse{Coupons: out, Total: total})
}

type createCouponReq struct {
	Code          string   `json:"code"`
	Name          string   `json:"name"`
	DiscountType  string   `json:"discount_type"`
	DiscountValue int64    `json:"discount_value"`
	Currency      string   `json:"currency"`
	MaxDiscount   *int64   `json:"max_discount"`
	MinOrder      int64    `json:"min_order_amount"`
	MaxRedeem     *int     `json:"max_redemptions"`
	MaxPerUser    *int     `json:"max_redemptions_per_user"`
	PlanIDs       []string `json:"applicable_plan_ids"`
	ValidFrom     string   `json:"valid_from"`
	ValidUntil    string   `json:"valid_until"`
}

type createCouponResponse struct {
	Code string `json:"code"`
	ID   string `json:"id"`
}

func (h *handlers) createCoupon(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req createCouponReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	from, until, perUser, err := normalizeCouponReq(&req, true)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	actor := httpx.PrincipalFrom(r.Context())
	var actorID *string
	if actor != nil && actor.UserID != "" {
		id := actor.UserID
		actorID = &id
	}

	newID, err := h.d.Billing.AdminCreateCoupon(r.Context(),
		couponSpec(tenantID, actorID, &req, from, until, perUser), req.Code, req.MaxRedeem)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, createCouponResponse{Code: req.Code, ID: newID})
}

// normalizeCouponReq 把券的公共字段规范化并校验一遍，返回解析后的
// 有效期和每人限领次数。
//
// 单张创建和批量生成共用同一份：折扣区间、币种默认值、时间解析这些
// 规则一旦两处各写一份，迟早会漂移成「单张创建拦得住、批量生成放得过」
// —— 而批量恰恰是错一次就错几百张的那一边。
//
// needCode 区分两条路径：单张创建必须自带券码；批量生成的券码是现场
// 随机出来的，请求里本来就没有。
func normalizeCouponReq(req *createCouponReq, needCode bool) (*time.Time, *time.Time, int, error) {
	req.Code = strings.TrimSpace(strings.ToUpper(req.Code))
	fields := map[string]string{}
	if needCode && req.Code == "" {
		fields["code"] = "必填"
	}
	if req.Name == "" {
		req.Name = req.Code
	}
	switch req.DiscountType {
	case "percent":
		// 万分比，与数据库约束一致。界面上填的是百分比，
		// 换算在提交那一层做 —— 和金额的元转分是同一处
		if req.DiscountValue < 1 || req.DiscountValue > 10000 {
			fields["discount_value"] = "折扣需在 0.01% 到 100% 之间"
		}
	case "fixed":
		if req.DiscountValue < 1 {
			fields["discount_value"] = "立减金额需大于 0"
		}
	default:
		fields["discount_type"] = "只支持 percent 或 fixed"
	}
	if len(fields) > 0 {
		return nil, nil, 0, httpx.Invalid(fields)
	}

	// 空串要变成 NULL 而不是零值时间：0001-01-01 会让「立即生效」
	// 的券看起来像是一千年前就开始了，排序和筛选都会跟着错
	parseTime := func(v string) (*time.Time, error) {
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			// 前端 datetime-local 不带时区，按服务器本地时间理解
			t, err = time.ParseInLocation("2006-01-02T15:04", v, time.Local)
			if err != nil {
				return nil, httpx.New(httpx.CodeValidationFailed, "时间格式不正确")
			}
		}
		return &t, nil
	}
	from, err := parseTime(req.ValidFrom)
	if err != nil {
		return nil, nil, 0, err
	}
	until, err := parseTime(req.ValidUntil)
	if err != nil {
		return nil, nil, 0, err
	}
	if from != nil && until != nil && !until.After(*from) {
		return nil, nil, 0, httpx.New(httpx.CodeValidationFailed, "结束时间必须晚于开始时间")
	}

	perUser := 1
	if req.MaxPerUser != nil && *req.MaxPerUser > 0 {
		perUser = *req.MaxPerUser
	}
	if req.Currency == "" {
		req.Currency = "CNY"
	}
	if req.PlanIDs == nil {
		req.PlanIDs = []string{}
	}
	return from, until, perUser, nil
}

// couponSpec 把规范化后的请求装成 billing 的券规格，单张创建与批量生成共用。
func couponSpec(tenantID string, actorID *string, req *createCouponReq,
	from, until *time.Time, perUser int) billing.AdminCouponSpec {
	return billing.AdminCouponSpec{
		TenantID: tenantID, ActorID: actorID,
		Name: req.Name, DiscountType: req.DiscountType, DiscountValue: req.DiscountValue,
		Currency: req.Currency, MaxDiscount: req.MaxDiscount, MinOrder: req.MinOrder,
		PerUser: perUser, PlanIDs: req.PlanIDs, ValidFrom: from, ValidUntil: until,
	}
}

type setCouponStatusResponse struct {
	OK bool `json:"ok"`
}

// setCouponStatus 启用或停用一张券。
func (h *handlers) setCouponStatus(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	// 不是 UUID 的 id 以前一路走到 SQL 的 ::uuid 上，报成 500
	if _, err := uuid.Parse(id); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// paused 而不是 disabled：数据库的状态枚举里没有后者，
	// 而 expired / exhausted 由系统自己置位，不接受手工设置
	if req.Status != "active" && req.Status != "paused" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "状态只能是 active 或 paused"))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}
	err := h.d.Billing.AdminSetCouponStatus(r.Context(), tenantID, actorID, id, req.Status)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, setCouponStatusResponse{OK: true})
}

type couponRedemptionsResponse struct {
	Redemptions []billing.AdminCouponRedemption `json:"redemptions"`
}

// couponRedemptions 是单张券的核销明细，用于核对。
func (h *handlers) couponRedemptions(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	out, err := h.d.Billing.AdminCouponRedemptions(r.Context(), tenantID, id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, couponRedemptionsResponse{Redemptions: out})
}
