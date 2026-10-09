package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

// registerCertificateRoutes 挂节点证书（P2）的后台路由。读挂 node.certificate.read；写挂
// node.certificate.write。录入或替换秘密（DNS 凭据、ZeroSSL EAB）与删除要近期重认证；
// 新建带幂等键（双击不建两张证书、两个凭据）。立即续期本身幂等（同一张证书同时只有一张进行中的
// 订单），不要幂等键。
func registerCertificateRoutes(r chi.Router, d Deps) {
	h := newCertHandlers(d)
	if h == nil {
		if d.Log != nil {
			d.Log.Error("没有信封加密器，节点证书的后台路由不挂")
		}
		return
	}
	read := middleware.RequirePermission("node.certificate.read", d.Log)
	write := middleware.RequirePermission("node.certificate.write", d.Log)

	r.With(read).Get("/certificates", h.list)
	r.With(read).Get("/certificates/{id}", h.detail)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.Idempotency(d.Pool, "certificate_create", d.Log),
	).Post("/certificates", h.create)
	r.With(write).Patch("/certificates/{id}", h.update)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/certificates/{id}", h.remove)
	r.With(write).Post("/certificates/{id}/renew", h.renew)
	r.With(write).Post("/certificates/{id}/pause", h.pause)
	r.With(write).Post("/certificates/{id}/resume", h.resume)

	r.With(read).Get("/dns-credentials", h.listCredentials)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "dns_credential_create", d.Log),
	).Post("/dns-credentials", h.createCredential)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Patch("/dns-credentials/{id}", h.updateCredential)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/dns-credentials/{id}", h.removeCredential)
	r.With(write).Post("/dns-credentials/{id}/verify", h.verifyCredential)

	r.With(read).Get("/settings/acme", h.acmeSettings)
	r.With(
		middleware.RequirePermission("node.certificate.write", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Put("/settings/acme", h.saveACMESettings)
}
