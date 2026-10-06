package admin

import (
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// validSiteTimezone 只收能加载的 IANA 名。空串与 "Local" 在 Go 里分别是 UTC
// 与「服务器本地时区」，都不是一个可以存下来、换台机器还一样的时区。
func validSiteTimezone(name string) bool {
	if name == "" || name == "Local" || len(name) > 64 {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

type getSiteSettingsResponse struct {
	Timezone string `json:"timezone"`
}

func (h *handlers) getSiteSettings(w http.ResponseWriter, r *http.Request) {
	tz, err := h.d.Ops.SiteTimezone(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, getSiteSettingsResponse{Timezone: tz})
}

type setSiteSettingsResponse struct {
	Timezone string `json:"timezone"`
}

func (h *handlers) setSiteSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req struct {
		Timezone string `json:"timezone"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if !validSiteTimezone(req.Timezone) {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"timezone": "不是有效的时区"}))
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}
	if err := h.d.Ops.SetSiteTimezone(r.Context(), tenantID, actorID, req.Timezone); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, setSiteSettingsResponse{Timezone: req.Timezone})
}
