package handler

import (
	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// pathUserID 解析路径里的 :user_id；不是 UUID 回 404。
func pathUserID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

// listOverrides 列出所有按用户单独设置的佣金规则。
func (h *Handler) listOverrides(c *gin.Context) {
	rows, err := h.svc.ListOverrides(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := make([]overrideResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, overrideFrom(r))
	}
	httpx.OK(c, listOverridesResponse{Overrides: out})
}

// setOverride 设置某用户的单独佣金规则（按邮箱匹配，覆盖更新）。
func (h *Handler) setOverride(c *gin.Context) {
	var req overrideRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ov, err := h.svc.SetOverride(c.Request.Context(), service.OverrideInput{
		Email:       req.Email,
		RatePercent: req.RatePercent,
		Scope:       req.Scope,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, setOverrideResponse{Override: overrideFrom(ov)})
}

// deleteOverride 删除某用户的单独佣金设置，恢复跟随全局。
func (h *Handler) deleteOverride(c *gin.Context) {
	id, ok := pathUserID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteOverride(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
