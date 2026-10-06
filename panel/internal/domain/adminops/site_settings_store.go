// [INPUT]: 依赖 platform 的 db 租户事务、audit 同事务审计、httpx 的错误模型与请求 ID；读写 tenants.timezone
// [OUTPUT]: 对外提供 Service.SiteTimezone / Service.SetSiteTimezone
// [POS]: domain/adminops 的站点设置存取（R49）：站点时区即 tenants.timezone，门户按日用量与后台收入趋势都按它切日；时区名校验留在 api/admin（只做请求形状），这里只管读写、行锁与审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// SiteTimezone 读租户的站点时区；租户行不可见时回中性 404。
func (s *Service) SiteTimezone(ctx context.Context, tenantID string) (string, error) {
	var tz string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT timezone FROM tenants WHERE id = $1`, tenantID).Scan(&tz)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NotFoundOrForbidden()
	}
	return tz, err
}

// SetSiteTimezone 锁住租户行改时区，同事务写 site.timezone_changed 前后对照审计。
// timezone 由调用方校验过（能被 time.LoadLocation 加载、不是空串与 Local）。
func (s *Service) SetSiteTimezone(ctx context.Context, tenantID string, actorID *string, timezone string) error {
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var old string
		if err := tx.QueryRow(ctx,
			`SELECT timezone FROM tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&old); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE tenants SET timezone = $2 WHERE id = $1`,
			tenantID, timezone); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: actorID,
			Action: "site.timezone_changed", ResourceType: "tenant", ResourceID: &tenantID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"timezone": old},
			AfterDigest:  map[string]any{"timezone": timezone},
		})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = httpx.NotFoundOrForbidden()
	}
	return err
}
