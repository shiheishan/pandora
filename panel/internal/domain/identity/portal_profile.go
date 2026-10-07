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
// 单条只读，经 readBatch 一次往返（不开 BEGIN / COMMIT）。
func (s *Service) PortalProfile(ctx context.Context, tenantID, userID string) (PortalProfile, error) {
	var out PortalProfile
	b := &pgx.Batch{}
	b.Queue(`SELECT email, display_name, status, created_at
		   FROM users WHERE tenant_id = $1 AND id = $2`, tenantID, userID).
		QueryRow(func(row pgx.Row) error {
			return row.Scan(&out.Email, &out.DisplayName, &out.Status, &out.CreatedAt)
		})
	err := s.readBatch(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, b)
	return out, err
}
