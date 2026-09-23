package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// 重认证：用当前口令换一枚 rat 刷新过的令牌，让高危操作重新可用。
//
// 为什么需要它：53 条写路由挂着 RequireRecentReauth（SEC-009），要求
// 令牌里的 rat（最近一次重认证时间）在 15 分钟以内。而 rat 只在登录那
// 一刻写入，此后永不更新，令牌本身却有 720 小时的寿命。
//
// 结果是登录满 15 分钟后，后台事实上变成只读：接入命令、编辑保存、
// 删除、开单、发公告全部报「此操作需要重新验证身份」，而系统里没有
// 任何地方能完成这个「重新验证」。唯一的出路是退出重登，可界面从没
// 这么提示过 —— 看上去就是一大片功能同时坏掉。
//
// 门是对的，只是从没装过钥匙。这里补上钥匙。
//
// 它刻意不签发新的 refresh token、不轮换会话：这不是一次新登录，
// 只是给同一个会话重新盖一次「刚刚确认过是本人」的时间戳。

type ReauthInput struct {
	UserID    string
	SessionID string
	Password  string
	Audience  string
	APIDomain string
	IPHash    []byte
	IP        string
	UserAgent string
}

type ReauthOutput struct {
	AccessToken string
	ExpiresIn   int
}

// Reauth 校验当前口令，成功后签发一枚 rat 为此刻的访问令牌。
//
// 口令错误的审计不能随事务错误一起回滚 —— 那样连续试错就不留痕迹了。
// 和 ChangePassword 一样：失败分支先提交审计，再在事务外返回认证错误。
func (s *Service) Reauth(ctx context.Context, tenantID string, in ReauthInput) (*ReauthOutput, error) {
	if tenantID == "" || in.UserID == "" || in.SessionID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "缺少会话信息，请重新登录")
	}
	if in.Password == "" {
		return nil, httpx.Invalid(map[string]string{"password": "请输入当前密码"})
	}

	userID := in.UserID
	apiDomain := in.APIDomain
	var resultErr error

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID},
		func(tx pgx.Tx) error {
			writeAudit := func(outcome, errorCode string) error {
				return audit.Write(ctx, tx, tenantID, audit.Entry{
					ActorKind:    "user",
					ActorID:      &userID,
					Action:       "user.reauthenticated",
					ResourceType: "user",
					ResourceID:   &userID,
					APIDomain:    apiDomain,
					Outcome:      outcome,
					ErrorCode:    errorCode,
					RequestID:    httpx.RequestIDFrom(ctx),
					SourceIP:     in.IP,
					UserAgent:    in.UserAgent,
				})
			}

			var phc string
			err := tx.QueryRow(ctx, `
				SELECT phc
				  FROM user_passwords
				 WHERE tenant_id = $1 AND user_id = $2::uuid`,
				tenantID, userID,
			).Scan(&phc)
			if errors.Is(err, pgx.ErrNoRows) {
				resultErr = httpx.New(httpx.CodeUnauthorized, "密码不正确")
				return writeAudit("failure", "invalid_password")
			}
			if err != nil {
				return err
			}

			ok, _, err := crypto.VerifyPassword(in.Password, phc)
			if err != nil {
				return err
			}
			if !ok {
				resultErr = httpx.New(httpx.CodeUnauthorized, "密码不正确")
				return writeAudit("failure", "invalid_password")
			}

			// 会话必须仍然有效。否则「重认证」就成了一条绕过登出、
			// 用旧 session id 换新令牌的路。
			var alive bool
			err = tx.QueryRow(ctx, `
				SELECT true
				  FROM sessions
				 WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid
				   AND revoked_at IS NULL AND expires_at > now()`,
				tenantID, in.SessionID, userID,
			).Scan(&alive)
			if errors.Is(err, pgx.ErrNoRows) {
				resultErr = httpx.New(httpx.CodeUnauthorized, "会话已失效，请重新登录")
				return writeAudit("failure", "session_invalid")
			}
			if err != nil {
				return err
			}

			return writeAudit("success", "")
		})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	if resultErr != nil {
		return nil, resultErr
	}

	access, err := s.issuer.Issue(token.Claims{
		Subject:   userID,
		TenantID:  tenantID,
		SessionID: in.SessionID,
		Kind:      "user",
		AuthMeth:  []string{"password"},
		ReauthAt:  time.Now().Unix(),
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}

	return &ReauthOutput{
		AccessToken: access,
		ExpiresIn:   int(s.issuer.TTL() / time.Second),
	}, nil
}
