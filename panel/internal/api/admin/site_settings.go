// [INPUT]: 依赖 platform 的 db/audit/httpx，依赖 time 的 IANA 时区库（经 domain/nodefabric 内嵌 time/tzdata）
// [OUTPUT]: 对外提供 handlers 的 getSiteSettings / setSiteSettings、validSiteTimezone
// [POS]: api/admin 的站点设置接口（契约后台-08，R49）：站点时区即 tenants.timezone，门户按日用量与后台收入趋势都按它切日
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
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

func (h *handlers) getSiteSettings(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var tz string
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT timezone FROM tenants WHERE id = $1`, tenantID).Scan(&tz)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NotFoundOrForbidden()
	}
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"timezone": tz})
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
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(r.Context(),
			`SELECT timezone FROM tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&old); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE tenants SET timezone = $2 WHERE id = $1`,
			tenantID, req.Timezone); err != nil {
			return err
		}
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "site.timezone_changed", ResourceType: "tenant", ResourceID: &tenantID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(r.Context()),
			BeforeDigest: map[string]any{"timezone": old},
			AfterDigest:  map[string]any{"timezone": req.Timezone},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NotFoundOrForbidden()
	}
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"timezone": req.Timezone})
}
