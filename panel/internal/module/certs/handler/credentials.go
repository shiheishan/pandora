package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func parseCredentialID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listDNSCredentials(c *gin.Context) {
	rows, err := h.svc.ListDNSCredentials(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]dnsCredentialResponse, len(rows))
	for i, r := range rows {
		items[i] = credentialFrom(r)
	}
	httpx.OK(c, dnsCredentialListResponse{Items: items})
}

func (h *Handler) createDNSCredential(c *gin.Context) {
	var req createDNSCredentialRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.CreateDNSCredential(c.Request.Context(), req.input())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, verifyResultFrom(out))
}

func (h *Handler) updateDNSCredential(c *gin.Context) {
	id, ok := parseCredentialID(c)
	if !ok {
		return
	}
	var req patchDNSCredentialRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.UpdateDNSCredential(c.Request.Context(), id, req.patch())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, verifyResultFrom(out))
}

func (h *Handler) deleteDNSCredential(c *gin.Context) {
	id, ok := parseCredentialID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteDNSCredential(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *Handler) verifyDNSCredential(c *gin.Context) {
	id, ok := parseCredentialID(c)
	if !ok {
		return
	}
	out, err := h.svc.VerifyDNSCredential(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, verifyResultFrom(out))
}

func (h *Handler) getACMESettings(c *gin.Context) {
	out, err := h.svc.ACMESettings(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, acmeFrom(out))
}

func (h *Handler) saveACMESettings(c *gin.Context) {
	var req acmeSettingsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.SaveACMESettings(c.Request.Context(), req.input())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, acmeFrom(out))
}
