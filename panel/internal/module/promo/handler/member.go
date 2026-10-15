package handler

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

type memberDiscountResponse struct {
	UserID    string     `json:"user_id"`
	Percent   *int       `json:"percent"`    // 没有设置为 null
	UpdatedAt *time.Time `json:"updated_at"` // 没有设置为 null
}

type setMemberDiscountRequest struct {
	Percent int `json:"percent"`
}

// getMemberDiscount 查看单个用户的会员折扣。
func (h *Handler) getMemberDiscount(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	view, err := h.svc.MemberDiscountOf(c.Request.Context(), userID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, memberDiscountResponse{
		UserID:    view.UserID.String(),
		Percent:   view.Percent,
		UpdatedAt: view.UpdatedAt,
	})
}

// setMemberDiscount 设置单个用户的会员折扣。
func (h *Handler) setMemberDiscount(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req setMemberDiscountRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	view, err := h.svc.SetMemberDiscount(c.Request.Context(), userID, req.Percent)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, memberDiscountResponse{
		UserID:    view.UserID.String(),
		Percent:   view.Percent,
		UpdatedAt: view.UpdatedAt,
	})
}

// clearMemberDiscount 清除单个用户的会员折扣。
func (h *Handler) clearMemberDiscount(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	if err := h.svc.ClearMemberDiscount(c.Request.Context(), userID); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
