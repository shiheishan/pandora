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
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type couponRow struct {
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

	out := []couponRow{}
	var total int64

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		const cond = `
			 WHERE c.tenant_id = $1
			   AND (lower(c.code::text) LIKE $2 OR lower(c.name) LIKE $2)
			   AND c.status LIKE $3`

		if err := tx.QueryRow(r.Context(),
			`SELECT count(*) FROM coupons c`+cond, tenantID, like, status).Scan(&total); err != nil {
			return err
		}

		rows, err := tx.Query(r.Context(), `
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
			var c couponRow
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
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"coupons": out, "total": total})
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

	var newID string
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			INSERT INTO coupons (tenant_id, code, name, discount_type, discount_value,
				currency, max_discount, min_order_amount, max_redemptions,
				max_redemptions_per_user, applicable_plan_ids, valid_from, valid_until,
				status, created_by)
			VALUES ($1,$2,$3,$4,$5,$6::app.currency_code,$7,$8,$9,$10,$11::uuid[],$12,$13,
				'active',$14::uuid)
			RETURNING id::text`,
			tenantID, req.Code, req.Name, req.DiscountType, req.DiscountValue,
			req.Currency, req.MaxDiscount, req.MinOrder, req.MaxRedeem,
			perUser, req.PlanIDs, from, until, actorID).Scan(&newID)
		if err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "coupon.created", ResourceType: "coupon", ResourceID: &newID,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{
				"code": req.Code, "type": req.DiscountType, "value": req.DiscountValue},
		})
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeConflict, "这个优惠码已经存在"))
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": newID, "code": req.Code})
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

// setCouponStatus 启用或停用一张券。
func (h *handlers) setCouponStatus(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
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

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			UPDATE coupons SET status = $3, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id, req.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NotFoundOrForbidden()
		}
		var actorID *string
		if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
			v := a.UserID
			actorID = &v
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "coupon.status_change", ResourceType: "coupon", ResourceID: &id,
			APIDomain: "admin", Outcome: "success",
			RequestID:   httpx.RequestIDFrom(r.Context()),
			AfterDigest: map[string]any{"status": req.Status},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ok": true})
}

// couponRedemptions 是单张券的核销明细，用于核对。
func (h *handlers) couponRedemptions(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	type item struct {
		Email    string `json:"email"`
		OrderNo  string `json:"order_no"`
		Discount int64  `json:"discount"`
		Currency string `json:"currency"`
		At       any    `json:"at"`
		Reverted bool   `json:"reverted"`
	}
	out := []item{}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
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
			var it item
			if err := rows.Scan(&it.Email, &it.OrderNo, &it.Discount, &it.Currency,
				&it.At, &it.Reverted); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"redemptions": out})
}
