package admin

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/certs"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 节点证书（P2：面板集中签发，DNS-01）：证书、DNS 凭据、ACME 设置
//
// 领域代码与 SQL 都在 domain/certs，这里只解请求体、取主体、写响应。凭据与 EAB 只写不读：
// 读接口的结构体里没有任何密文字段，PATCH 里缺席的字段保留原值。
//------------------------------------------------------------------------------

type certHandlers struct {
	svc *certs.Service
	log *slog.Logger
}

func newCertHandlers(d Deps) *certHandlers {
	opts := certs.Options{Log: d.Log}
	if d.Cfg != nil {
		opts.DirectoryOverride = d.Cfg.ACME.DirectoryOverride
		opts.TrustedRoots = d.Cfg.ACME.TrustedRoots
	}
	var sealer certs.Sealer
	if d.Envelope != nil {
		sealer = d.Envelope
	}
	return &certHandlers{svc: certs.NewService(d.Pool, sealer, opts), log: d.Log}
}

func certActor(r *http.Request) certs.Actor {
	return certs.Actor{Kind: "admin", ID: httpx.PrincipalFrom(r.Context()).UserID}
}

func (h *certHandlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Fail(w, r, h.log, err)
}

// --- 证书 ---

func (h *certHandlers) list(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ListCertificates(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *certHandlers) detail(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.CertificateDetail(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *certHandlers) create(w http.ResponseWriter, r *http.Request) {
	var in certs.CertificateInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.svc.CreateCertificate(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *certHandlers) update(w http.ResponseWriter, r *http.Request) {
	var in certs.CertificatePatch
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.svc.UpdateCertificate(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *certHandlers) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteCertificate(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

// renew 立刻排一张订单（202：签发由 worker 异步完成）。
func (h *certHandlers) renew(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.RenewCertificate(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

func (h *certHandlers) pause(w http.ResponseWriter, r *http.Request)  { h.setPaused(w, r, true) }
func (h *certHandlers) resume(w http.ResponseWriter, r *http.Request) { h.setPaused(w, r, false) }

func (h *certHandlers) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	out, err := h.svc.SetCertificatePaused(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id"), paused)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// --- DNS 凭据 ---

type dnsCredentialListResponse struct {
	Items []certs.DNSCredential `json:"items"`
}

func (h *certHandlers) listCredentials(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ListDNSCredentials(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, dnsCredentialListResponse{Items: out})
}

func (h *certHandlers) createCredential(w http.ResponseWriter, r *http.Request) {
	var in certs.DNSCredentialInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.svc.CreateDNSCredential(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Created(w, out)
}

func (h *certHandlers) updateCredential(w http.ResponseWriter, r *http.Request) {
	var in certs.DNSCredentialPatch
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.svc.UpdateDNSCredential(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *certHandlers) removeCredential(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteDNSCredential(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id")); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}

func (h *certHandlers) verifyCredential(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.VerifyDNSCredential(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r),
		chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

// --- ACME 设置 ---

func (h *certHandlers) acmeSettings(w http.ResponseWriter, r *http.Request) {
	out, err := h.svc.ACMESettings(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}

func (h *certHandlers) saveACMESettings(w http.ResponseWriter, r *http.Request) {
	var in certs.ACMESettingsInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	out, err := h.svc.SaveACMESettings(r.Context(), httpx.TenantIDFrom(r.Context()), certActor(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, out)
}
