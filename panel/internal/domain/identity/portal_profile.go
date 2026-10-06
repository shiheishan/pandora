// [INPUT]: 依赖 platform/db 的租户事务，读 users
// [OUTPUT]: 对外提供 PortalProfile、Service.PortalProfile
// [POS]: domain/identity 的门户账户行（门户 GET v1/me），从 api/public/handlers.go 下沉；管理员那一份在 admin_profile.go，两者口径不同（门户不带角色）

package identity

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// PortalProfile 是门户 me 接口要的账户字段；user_id 与权限取自令牌，不在这里。
type PortalProfile struct {
	Email       string
	DisplayName *string
	Status      string
	CreatedAt   time.Time
}

// PortalProfile 读本人的账户行；行不存在按错误返回（pgx.ErrNoRows），由调用方决定怎么报。
func (s *Service) PortalProfile(ctx context.Context, tenantID, userID string) (PortalProfile, error) {
	var out PortalProfile
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT email, display_name, status, created_at
				   FROM users WHERE tenant_id = $1 AND id = $2`,
				tenantID, userID).Scan(&out.Email, &out.DisplayName, &out.Status, &out.CreatedAt)
		})
	return out, err
}
