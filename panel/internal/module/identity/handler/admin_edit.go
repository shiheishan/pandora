package handler

import (
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type admSetEmailRequest struct {
	Email  string `json:"email" binding:"required,max=320"`
	Reason string `json:"reason" binding:"max=1000"`
}

type admSetNoteRequest struct {
	DisplayName *string `json:"display_name" binding:"omitempty,max=400"`
	AdminNote   *string `json:"admin_note" binding:"omitempty,max=8000"`
}

type admDeleteUserRequest struct {
	Reason string `json:"reason" binding:"max=1000"`
}

// admBulkBanRequest 筛选字段与批量预览相同，内嵌 bulkFilterRequest。
type admBulkBanRequest struct {
	bulkFilterRequest
	ExpectedTotal int64  `json:"expected_total" binding:"min=0"`
	Reason        string `json:"reason" binding:"max=1000"`
}

type admSetEmailResponse struct {
	OK    bool   `json:"ok"`
	Email string `json:"email"`
}

type admDeleteUserResponse struct {
	OK                      bool   `json:"ok"`
	ShortID                 string `json:"short_id"`
	TerminatedSubscriptions int    `json:"terminated_subscriptions"`
}

type admBulkBanResponse struct {
	Banned  int            `json:"banned"`
	Skipped map[string]int `json:"skipped"`
}

// setUserEmail 对应 POST /v1/users/:id/email
func (h *Handler) setUserEmail(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admSetEmailRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.AdminSetEmail(c.Request.Context(), id, req.Email, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	normalized := strings.ToLower(strings.TrimSpace(req.Email))
	httpx.OK(c, admSetEmailResponse{OK: true, Email: normalized})
}

// setUserNote 对应 POST /v1/users/:id/note
func (h *Handler) setUserNote(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admSetNoteRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.AdminSetNote(c.Request.Context(), id, req.DisplayName, req.AdminNote); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admOKResponse{OK: true})
}

// deleteUser 对应 POST /v1/users/:id/delete
func (h *Handler) deleteUser(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admDeleteUserRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.DeleteUser(c.Request.Context(), id, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admDeleteUserResponse{
		OK:                      true,
		ShortID:                 res.ShortID,
		TerminatedSubscriptions: res.TerminatedSubscriptions,
	})
}

// bulkBan 对应 POST /v1/users/bulk/ban
func (h *Handler) bulkBan(c *gin.Context) {
	var req admBulkBanRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	f, err := req.bulkFilterRequest.toFilter()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.BulkBan(c.Request.Context(), f, req.ExpectedTotal, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	skipped := res.Skipped
	if skipped == nil {
		skipped = map[string]int{}
	}
	httpx.OK(c, admBulkBanResponse{Banned: res.Banned, Skipped: skipped})
}
