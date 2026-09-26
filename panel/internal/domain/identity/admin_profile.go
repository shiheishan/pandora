// [INPUT]: 依赖 platform/db 的租户事务，读 users 与 role_bindings→roles
// [OUTPUT]: 对外提供 AdminProfile、RoleRef、Service.AdminProfile
// [POS]: domain/identity 的管理员身份展示（契约后台外壳 GET v1/me 追加字段）：邮箱、显示名与当前生效的角色，角色过滤与 Login 展开权限时一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
func (s *Service) AdminProfile(ctx context.Context, tenantID, userID string) (*AdminProfile, error) {
	out := &AdminProfile{Roles: []RoleRef{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT email::text, nullif(btrim(display_name), '') FROM users
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, userID).
			Scan(&out.Email, &out.DisplayName); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT r.code, r.name
			  FROM role_bindings rb
			  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
			 WHERE rb.tenant_id = $1 AND rb.user_id = $2::uuid
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			   AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
			 ORDER BY r.name, r.code`, tenantID, userID)
		if err != nil {
			return err
		}
		out.Roles, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RoleRef])
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeUnauthorized, "需要登录")
	}
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}
