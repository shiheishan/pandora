package handler

import (
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func parseCertID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listCertificates(c *gin.Context) {
	out, err := h.svc.ListCertificates(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, listFrom(out))
}

func (h *Handler) certificateDetail(c *gin.Context) {
	id, ok := parseCertID(c)
	if !ok {
		return
	}
	out, err := h.svc.CertificateDetail(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, detailFrom(out))
}

func (h *Handler) createCertificate(c *gin.Context) {
	var req createCertificateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.CreateCertificate(c.Request.Context(), req.input())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, certificateFrom(out))
}

func (h *Handler) updateCertificate(c *gin.Context) {
	id, ok := parseCertID(c)
	if !ok {
		return
	}
	var req patchCertificateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.UpdateCertificate(c.Request.Context(), id, req.patch())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, certificateFrom(out))
}

func (h *Handler) deleteCertificate(c *gin.Context) {
	id, ok := parseCertID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteCertificate(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.NoContent(c)
}

func (h *Handler) renewCertificate(c *gin.Context) {
	id, ok := parseCertID(c)
	if !ok {
		return
	}
	out, err := h.svc.RenewCertificate(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Accepted(c, orderBriefFrom(out))
}

func (h *Handler) pauseCertificate(c *gin.Context) {
	h.setCertificatePaused(c, true)
}

func (h *Handler) resumeCertificate(c *gin.Context) {
	h.setCertificatePaused(c, false)
}

func (h *Handler) setCertificatePaused(c *gin.Context, paused bool) {
	id, ok := parseCertID(c)
	if !ok {
		return
	}
	out, err := h.svc.SetCertificatePaused(c.Request.Context(), id, paused)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, certificateFrom(out))
}
