package handler

import (
	"github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// previewCoupon 是门户下单前的优惠码试算：{plan_id, price_id} 试算套餐。
// 金额由服务端按价格查出，不接受前端传值。
func (h *Handler) previewCoupon(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok || p.UserID == uuid.Nil {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	var req previewRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}

	// 空串或不是 UUID 的值在这里挡掉，回字段级 422，而不是一路传下去
	fields := map[string]string{}
	var item service.PreviewItem
	if id, err := uuid.Parse(req.PlanID); err != nil {
		fields["plan_id"] = "必填"
	} else {
		item.PlanID = id
	}
	if id, err := uuid.Parse(req.PriceID); err != nil {
		fields["price_id"] = "必填"
	} else {
		item.PriceID = id
	}
	if len(fields) > 0 {
		httpx.Fail(c, errs.Invalid(fields))
		return
	}

	out, err := h.svc.Preview(c.Request.Context(), p.UserID, req.CouponCode, item)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := previewFrom(out)
	if err != nil {
		httpx.Fail(c, errs.Wrap(errs.Internal, err))
		return
	}
	httpx.OK(c, resp)
}
