// Package handler 是 certs 模块的 HTTP 层：后台证书、DNS 凭据与 ACME 设置。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/certs/service"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 certs service 方法；*service.Service 实现它。
type Service interface {
	ListCertificates(ctx context.Context) (service.CertificateList, error)
	CertificateDetail(ctx context.Context, id uuid.UUID) (service.CertificateDetail, error)
	CreateCertificate(ctx context.Context, in service.CertificateInput) (service.Certificate, error)
	UpdateCertificate(ctx context.Context, id uuid.UUID, p service.CertificatePatch) (service.Certificate, error)
	DeleteCertificate(ctx context.Context, id uuid.UUID) error
	RenewCertificate(ctx context.Context, id uuid.UUID) (service.OrderBrief, error)
	SetCertificatePaused(ctx context.Context, id uuid.UUID, paused bool) (service.Certificate, error)
	ListDNSCredentials(ctx context.Context) ([]service.DNSCredential, error)
	CreateDNSCredential(ctx context.Context, in service.DNSCredentialInput) (service.VerifyResult, error)
	UpdateDNSCredential(ctx context.Context, id uuid.UUID, p service.DNSCredentialPatch) (service.VerifyResult, error)
	DeleteDNSCredential(ctx context.Context, id uuid.UUID) error
	VerifyDNSCredential(ctx context.Context, id uuid.UUID) (service.VerifyResult, error)
	ACMESettings(ctx context.Context) (service.ACMESettings, error)
	SaveACMESettings(ctx context.Context, in service.ACMESettingsInput) (service.ACMESettings, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 certs 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterAdmin 注册后台证书与 DNS 路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/certificates", k.RequireAuth, h.listCertificates)
	v1.GET("/certificates/:id", k.RequireAuth, h.certificateDetail)
	v1.POST("/certificates", k.RequireAuth, k.Idempotency("certificate_create"), h.createCertificate)
	v1.PATCH("/certificates/:id", k.RequireAuth, h.updateCertificate)
	v1.DELETE("/certificates/:id", k.RequireAuth, k.RecentReauth, h.deleteCertificate)
	v1.POST("/certificates/:id/renew", k.RequireAuth, h.renewCertificate)
	v1.POST("/certificates/:id/pause", k.RequireAuth, h.pauseCertificate)
	v1.POST("/certificates/:id/resume", k.RequireAuth, h.resumeCertificate)

	v1.GET("/dns-credentials", k.RequireAuth, h.listDNSCredentials)
	v1.POST("/dns-credentials", k.RequireAuth, k.RecentReauth, k.Idempotency("dns_credential_create"), h.createDNSCredential)
	v1.PATCH("/dns-credentials/:id", k.RequireAuth, k.RecentReauth, h.updateDNSCredential)
	v1.DELETE("/dns-credentials/:id", k.RequireAuth, k.RecentReauth, h.deleteDNSCredential)
	v1.POST("/dns-credentials/:id/verify", k.RequireAuth, h.verifyDNSCredential)

	v1.GET("/settings/acme", k.RequireAuth, h.getACMESettings)
	v1.PUT("/settings/acme", k.RequireAuth, k.RecentReauth, h.saveACMESettings)
}

// RegisterPublic 门户无证书管理接口。
func (h *Handler) RegisterPublic(_ *gin.RouterGroup, _ route.Kit) {}
