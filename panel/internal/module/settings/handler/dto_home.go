package handler

import (
	"github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

type homeFAQItem struct {
	Q string `json:"q"`
	A string `json:"a"`
}

// homeResponse 是首页设置；faq 永远是数组（没有时是 []）。
type homeResponse struct {
	Enabled bool          `json:"enabled"`
	Intro   string        `json:"intro"`
	FAQ     []homeFAQItem `json:"faq"`
}

// setHomeRequest 是首页设置的部分更新：缺的字段保持原值；faq 给了就整组替换。
type setHomeRequest struct {
	Enabled *bool          `json:"enabled"`
	Intro   *string        `json:"intro"`
	FAQ     *[]homeFAQItem `json:"faq"`
}

func homeToResponse(v service.Home) homeResponse {
	faq := make([]homeFAQItem, len(v.FAQ))
	for i, item := range v.FAQ {
		faq[i] = homeFAQItem{Q: item.Q, A: item.A}
	}
	return homeResponse{
		Enabled: v.Enabled,
		Intro:   v.Intro,
		FAQ:     faq,
	}
}

func (h *Handler) getHome(c *gin.Context) {
	ctx := c.Request.Context()
	v, err := h.svc.Home(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, homeToResponse(v))
}

func (h *Handler) setHome(c *gin.Context) {
	ctx := c.Request.Context()
	var req setHomeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	var faq *[]service.HomeFAQ
	if req.FAQ != nil {
		out := make([]service.HomeFAQ, len(*req.FAQ))
		for i, item := range *req.FAQ {
			out[i] = service.HomeFAQ{Q: item.Q, A: item.A}
		}
		faq = &out
	}
	if err := h.svc.SetHome(ctx, service.HomeUpdate{
		Enabled: req.Enabled,
		Intro:   req.Intro,
		FAQ:     faq,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 回写入后的值，去过首尾空白，后台据此刷新
	v, err := h.svc.Home(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, homeToResponse(v))
}
