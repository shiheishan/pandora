package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/credentialrevocation"
)

// revokeAllLogins 在调用方事务里让一个账号的全部登录凭据立即失效：会话、刷新令牌、
// 刷新令牌家族三步缺一不可——少了刷新令牌，旧凭据还能绕过会话吊销换新的访问令牌。
//
// 后台替人重置密码与自助找回密码共用这一份；门户自助改密要保留当前会话，
// 单独写在 ChangePassword 里。快捷登录令牌绑定签发它的会话，会话一吊销它就跟着作废。
func revokeAllLogins(ctx context.Context, tx pgx.Tx, tenantID, userID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = now(), revoked_reason = $3
		 WHERE tenant_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL`,
		tenantID, userID, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE refresh_tokens
		   SET status = 'revoked'
		 WHERE tenant_id = $1 AND user_id = $2::uuid AND status = 'active'`,
		tenantID, userID); err != nil {
		return err
	}
	_, err := credentialrevocation.RevokeRefreshFamilies(ctx, tx, tenantID, userID)
	return err
}
