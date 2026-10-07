package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type RoleRef struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type AdminProfile struct {
	Email       string    `json:"email"`
	DisplayName *string   `json:"display_name"`
	Roles       []RoleRef `json:"roles"`
}

// AdminProfile 读管理员的展示信息。角色只算租户级、未过期的绑定——与 admin 域
// 登录展开权限用的过滤相同，否则侧栏显示的角色和实际能做的事会对不上。
//
// 两条读互不依赖，经 readBatch 一次往返发出（注入租户与两条查询同批），不再开 BEGIN / COMMIT。
func (s *Service) AdminProfile(ctx context.Context, tenantID, userID string) (*AdminProfile, error) {
	out := &AdminProfile{Roles: []RoleRef{}}
	b := &pgx.Batch{}
	b.Queue(`
			SELECT email::text, nullif(btrim(display_name), '') FROM users
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userID).
		QueryRow(func(row pgx.Row) error { return row.Scan(&out.Email, &out.DisplayName) })
	b.Queue(`
			SELECT DISTINCT r.code, r.name
			  FROM role_bindings rb
			  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
			 WHERE rb.tenant_id = $1 AND rb.user_id = $2::uuid
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			   AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
			 ORDER BY r.name, r.code`, tenantID, userID).
		Query(func(rows pgx.Rows) error {
			roles, err := pgx.CollectRows(rows, pgx.RowToStructByPos[RoleRef])
			if err == nil && roles != nil {
				out.Roles = roles
			}
			return err
		})
	err := s.readBatch(ctx, db.Scope{TenantID: tenantID}, b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeUnauthorized, "需要登录")
	}
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}
