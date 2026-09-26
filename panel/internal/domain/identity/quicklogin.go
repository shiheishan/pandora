// [INPUT]: 依赖 platform 的 audit/crypto/db/httpx/token，依赖 google/uuid
// [OUTPUT]: 对外提供 QuickLoginTTL、QuickLoginOutput、IssueQuickLogin、ConsumeQuickLogin
// [POS]: domain/identity 的快捷登录：门户已登录设备签发 60 秒一次性链接，库里只存哈希且绑定签发会话，会话失效链接即作废
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// 快捷登录（对标 Xboard getQuickLoginUrl）。
//
// 用户在已登录的面板里点一下，拿到一条 60 秒内有效的链接，
// 在另一台设备打开即进入自己的账号 —— 免去在手机上敲一遍长密码。
//
// 这是一条能直接换到身份的凭证，所以每一处都按最严的来：
//   · 明文只在生成的那一刻返回一次，库里只留哈希
//   · 60 秒过期，一次性
//   · 绑定签发它的会话：原会话退出或被踢，这条链接立刻作废
//
// 最后一条尤其重要。不绑会话的话，「退出登录」就成了假动作 ——
// 手里还攥着链接的人照样进得来，而用户以为自己已经登出了。

// QuickLoginTTL 刻意短。它只需要覆盖「复制链接 → 换台设备打开」这段时间。
const QuickLoginTTL = 60 * time.Second

type QuickLoginOutput struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
}

func (s *Service) IssueQuickLogin(ctx context.Context, tenantID, userID,
	sessionID string) (*QuickLoginOutput, error) {

	if _, err := uuid.Parse(userID); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "用户标识不正确")
	}
	if _, err := uuid.Parse(sessionID); err != nil {
		// 没有会话就没有可绑定的生命周期，这条链接会活得比登录状态还久
		return nil, httpx.New(httpx.CodeUnauthorized, "当前登录状态无法签发快捷登录链接")
	}

	raw, err := crypto.NewToken(32)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(QuickLoginTTL)

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		// 以源会话行为串行化点。quick_login_tokens 只有 token_hash 唯一键，
		// 单纯 DELETE 再 INSERT 会让两个并发请求都在各自快照里删空后各插一条。
		// 锁住同一个 session 后，后来的签发必须等前一个提交，再删除前一条。
		var lockedSessionID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM sessions
			 WHERE tenant_id=$1 AND id=$2::uuid AND user_id=$3::uuid
		 FOR UPDATE`, tenantID, sessionID, userID).Scan(&lockedSessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeUnauthorized, "当前登录状态无法签发快捷登录链接")
			}
			return err
		}
		// 同一个会话只保留最新一条：用户连点两次「生成链接」，
		// 前一条应当立刻作废，否则散在外面的有效链接会越积越多。
		if _, err := tx.Exec(ctx, `
			DELETE FROM quick_login_tokens
			 WHERE tenant_id=$1 AND session_id=$2::uuid AND used_at IS NULL`,
			tenantID, sessionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO quick_login_tokens
				(tenant_id, user_id, session_id, token_hash, expires_at)
			VALUES ($1,$2::uuid,$3::uuid,$4,$5)`,
			tenantID, userID, sessionID, crypto.HashToken(raw), expires)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &QuickLoginOutput{
		Token: raw, ExpiresAt: expires, ExpiresIn: int(QuickLoginTTL.Seconds()),
	}, nil
}

// ConsumeQuickLogin 用快捷登录令牌换一套正常的访问凭证。
//
// 换出来的是一个全新的会话，而不是复用签发方的会话 —— 新设备应当能
// 在「我的登录设备」里被单独看到、单独踢掉。共用一个会话的话，
// 用户想踢掉手机就会把电脑也一起踢了。
func (s *Service) ConsumeQuickLogin(ctx context.Context, tenantID, rawToken,
	userAgent string, ipHash []byte) (*LoginOutput, error) {

	if rawToken == "" {
		return nil, httpx.New(httpx.CodeUnauthorized, "快捷登录链接无效或已过期")
	}

	var out LoginOutput
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var tokenID, userID, srcSessionID string
		// 一并校验签发它的会话还活着。这一步让「退出登录」真正生效。
		err := tx.QueryRow(ctx, `
			SELECT q.id::text, q.user_id::text, q.session_id::text
			  FROM quick_login_tokens q
			  JOIN sessions s ON s.tenant_id=q.tenant_id AND s.id=q.session_id
			 WHERE q.tenant_id=$1 AND q.token_hash=$2
			   AND q.used_at IS NULL AND q.expires_at > now()
			   AND s.revoked_at IS NULL
			   AND (s.expires_at IS NULL OR s.expires_at > now())
			 FOR UPDATE OF q`, tenantID, crypto.HashToken(rawToken)).
			Scan(&tokenID, &userID, &srcSessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeUnauthorized, "快捷登录链接无效或已过期")
		}
		if err != nil {
			return err
		}

		// 先标记已用再签发。反过来的话，签发成功但标记失败会留下
		// 一条还能再用一次的链接。
		tag, err := tx.Exec(ctx, `
			UPDATE quick_login_tokens SET used_at = now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND used_at IS NULL`,
			tenantID, tokenID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			// 并发消费：另一个请求抢先用掉了
			return httpx.New(httpx.CodeUnauthorized, "快捷登录链接无效或已过期")
		}

		var status string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM users WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, userID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return httpx.New(httpx.CodeForbidden, "账号当前不可登录")
		}

		now := time.Now().UTC()
		var sessionID string
		// auth_methods 记成 quick_login 而不是 password：这次进入没有
		// 重新验证过口令，风控和「需要近期重认证」的接口应当能区分出来。
		if err := tx.QueryRow(ctx, `
			INSERT INTO sessions
				(tenant_id, user_id, audience, user_agent, ip_hash, auth_methods,
				 last_reauth_at, expires_at)
			VALUES ($1,$2::uuid,'public',$3,$4,ARRAY['quick_login'],NULL,$5)
			RETURNING id::text`,
			tenantID, userID, userAgent, ipHash, now.Add(s.refreshTTL)).
			Scan(&sessionID); err != nil {
			return err
		}

		refresh, err := crypto.NewToken(32)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO refresh_tokens
				(tenant_id, session_id, user_id, token_hash, expires_at)
			VALUES ($1,$2::uuid,$3::uuid,$4,$5)`,
			tenantID, sessionID, userID, crypto.HashToken(refresh),
			now.Add(s.refreshTTL)); err != nil {
			return err
		}

		access, err := s.issuer.Issue(token.Claims{
			Subject: userID, TenantID: tenantID, SessionID: sessionID,
			Audience: "public", Kind: "user",
		})
		if err != nil {
			return err
		}
		out = LoginOutput{
			AccessToken: access, RefreshToken: refresh,
			ExpiresIn: int(s.issuer.TTL().Seconds()), UserID: userID,
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "auth.quick_login", ResourceType: "session",
			ResourceID: &sessionID,
			AfterDigest: map[string]any{
				"source_session": srcSessionID, "auth_method": "quick_login",
			},
			APIDomain: "public", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
