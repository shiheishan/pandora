// [INPUT]: 依赖 platform/db 的租户事务、platform/audit、platform/httpx
// [OUTPUT]: 对外提供 SessionInfo、ListActiveSessions、RevokeSession
// [POS]: domain/identity 的门户自助会话管理：只列出、只吊销 audience=public 的会话；last_seen_at 由 middleware/auth.go 节流刷新（R62），这里只读
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户自助管理登录会话（对标 Xboard getActiveSession / removeActiveSession）。
//
// 这是个安全功能：用户在网吧或别人电脑上登录过，回家想把那个会话踢掉，
// 而不是改密码把所有设备都踹下线。看得见「哪些地方登录着」本身也是
// 发现账号被盗的第一手线索。
//
// 会话 ID 完整返回。它不是 bearer token —— 访问令牌是签过名的 JWT，
// 会话 ID 只是里面的一个字段，单独拿着它换不出任何权限。而且这些会话
// 本来就属于请求者自己。
//
// （最初写成只回前 8 位，是想少暴露点东西。但会话 ID 是 UUIDv7，
// 前几位是时间戳 —— 同一秒建的会话前缀完全一样，用它当标识
// 会让「踢掉某个会话」变成一件靠运气的事。）
//
// IP 只回国家，不回明文地址：库里存的本来就是哈希，
// 而且用户的 IP 列表若被他人看到，等于泄漏行踪。
//
// 只看得见、也只踢得掉门户（public）会话。同一个人可能也是管理员：
// 门户令牌若能列出并吊销后台会话，一枚泄漏的门户令牌就能把管理员
// 踢下线、还能看到后台登录的设备与时间 —— 两个域互不越界（ARC-002）。

// selfServiceAudience 是门户自助会话管理能触及的唯一 audience。
const selfServiceAudience = "public"

type SessionInfo struct {
	ID         string     `json:"id"`
	Current    bool       `json:"current"`
	UserAgent  string     `json:"user_agent"`
	Country    string     `json:"country,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func (s *Service) ListActiveSessions(ctx context.Context, tenantID, userID,
	currentSessionID string) ([]SessionInfo, error) {

	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "user identifier is invalid")
	}
	out := []SessionInfo{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, coalesce(user_agent,''), coalesce(ip_country,''),
			       created_at, last_seen_at, expires_at
			  FROM sessions
			 WHERE tenant_id=$1 AND user_id=$2::uuid AND audience=$3
			   AND revoked_at IS NULL
			   AND (expires_at IS NULL OR expires_at > now())
			 ORDER BY last_seen_at DESC NULLS LAST, created_at DESC
			 LIMIT 50`, tenantID, userID, selfServiceAudience)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var si SessionInfo
			if err := rows.Scan(&si.ID, &si.UserAgent, &si.Country,
				&si.CreatedAt, &si.LastSeenAt, &si.ExpiresAt); err != nil {
				return err
			}
			si.Current = si.ID == currentSessionID
			out = append(out, si)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeSession 踢掉一个会话。
//
// 只能踢自己的门户会话：WHERE 里带 user_id 与 audience，别人的会话、
// 自己的后台会话传进来都当作不存在。
func (s *Service) RevokeSession(ctx context.Context, tenantID, userID,
	target, currentSessionID string) error {

	if _, err := uuid.Parse(target); err != nil {
		return httpx.NotFoundOrForbidden()
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM sessions
				 WHERE tenant_id=$1 AND user_id=$2::uuid AND id=$3::uuid
				   AND audience=$4 AND revoked_at IS NULL)`,
			tenantID, userID, target, selfServiceAudience).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NotFoundOrForbidden()
		}
		if target == currentSessionID {
			return httpx.New(httpx.CodeValidationFailed,
				"这是你当前正在使用的会话。要退出当前设备请直接点退出登录")
		}

		tag, err := tx.Exec(ctx, `
			UPDATE sessions
			   SET revoked_at = now(), revoked_reason = 'user_revoked'
			 WHERE tenant_id=$1 AND id=$2::uuid AND audience=$3 AND revoked_at IS NULL`,
			tenantID, target, selfServiceAudience)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("session revoke transition lost")
		}

		// 刷新令牌也要一起废掉。只吊销会话的话，那台设备手里的
		// refresh token 还能换出新的访问令牌 —— 等于没踢。
		if _, err := tx.Exec(ctx, `
			UPDATE refresh_tokens SET status = 'revoked'
			 WHERE tenant_id=$1 AND session_id=$2::uuid AND status = 'active'`,
			tenantID, target); err != nil {
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "session.revoked_by_user", ResourceType: "session",
			ResourceID:  &target,
			AfterDigest: map[string]any{"reason": "user_revoked"},
			APIDomain:   "public", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
}
