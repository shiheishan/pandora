package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// subID 解析路径里的订阅 id；格式不对按不存在处理。
func subID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

// mySubscriptions 是门户「我的套餐」。
func (h *Handler) mySubscriptions(c *gin.Context) {
	res, err := h.svc.MySubscriptions(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := mySubscriptionsFrom(res)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// renameSubscription 给本人的一份订阅起名、改名或清除名字。
func (h *Handler) renameSubscription(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req renameRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.SetMyLabel(c.Request.Context(), id, req.Label)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, renameResponse{Label: res.Label, ClientName: res.ClientName})
}

// myTrafficPacks 是本人的流量包余额与明细。
func (h *Handler) myTrafficPacks(c *gin.Context) {
	res, err := h.svc.MyTrafficPacks(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, packBalanceFrom(res))
}

// transferTrafficPacks 把未分配或已停用那份上的流量包转到一份在用的上。
func (h *Handler) transferTrafficPacks(c *gin.Context) {
	var req transferRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	to, err := uuid.Parse(req.ToSubscriptionID)
	if err != nil {
		httpx.Fail(c, errs.Invalid(map[string]string{"to_subscription_id": "请选择转到哪一份"}))
		return
	}
	var from *uuid.UUID
	if req.FromSubscriptionID != nil {
		id, err := uuid.Parse(*req.FromSubscriptionID)
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"from_subscription_id": "格式不正确"}))
			return
		}
		from = &id
	}
	moved, err := h.svc.MyTransferPacks(c.Request.Context(), from, to)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, transferResponse{MovedBytes: moved})
}
