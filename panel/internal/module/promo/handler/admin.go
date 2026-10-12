package handler

import (
	"strings"

	"github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listCoupons 按券码或名称搜索、按状态筛选并分页。
func (h *Handler) listCoupons(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 25, 200)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var q listCouponsQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, total, err := h.svc.ListCoupons(c.Request.Context(), service.ListFilter{Query: q.Q, Status: q.Status}, limit, offset)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]couponResponse, len(rows))
	for i, r := range rows {
		out[i] = couponFrom(r)
	}
	httpx.OK(c, listCouponsResponse{Coupons: out, Total: total})
}

// couponRedemptions 是单张券的核销明细。
func (h *Handler) couponRedemptions(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	rows, err := h.svc.Redemptions(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]redemptionResponse, len(rows))
	for i, r := range rows {
		out[i] = redemptionFrom(r)
	}
	httpx.OK(c, redemptionsResponse{Redemptions: out})
}

// createCoupon 新建一张券；名称缺省为券码。
func (h *Handler) createCoupon(c *gin.Context) {
	var req couponRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		httpx.Fail(c, errs.Invalid(map[string]string{"code": "请填写优惠码"}))
		return
	}
	spec, err := req.spec()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if spec.Name == "" {
		spec.Name = code
	}
	id, saved, err := h.svc.CreateCoupon(c.Request.Context(), req.Code, spec)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, createCouponResponse{Code: saved, ID: id.String()})
}

// generateCoupons 一次生成多张配置相同、券码不同的券；每张缺省只能用一次。
func (h *Handler) generateCoupons(c *gin.Context) {
	var req generateCouponsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	spec, err := req.spec()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if spec.MaxRedemptions == nil || *spec.MaxRedemptions <= 0 {
		one := 1
		spec.MaxRedemptions = &one
	}
	codes, err := h.svc.GenerateCoupons(c.Request.Context(), req.Prefix, req.Count, spec)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, generateCouponsResponse{Codes: codes, Count: len(codes), Name: spec.Name})
}

// setCouponStatus 启用或停用一张券。
func (h *Handler) setCouponStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req setStatusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetStatus(c.Request.Context(), id, req.Status); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
