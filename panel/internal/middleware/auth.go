// [INPUT]: 依赖 platform/token 的令牌校验、platform/db 的租户事务、platform/httpx 的 Principal 与错误模型；读 sessions / role_bindings / roles / role_permissions，写 sessions.last_seen_at
// [OUTPUT]: 对外提供 Authenticate（解析 Bearer 并装配 Principal）
// [POS]: middleware 的认证入口：会话有效性、节流刷新 last_seen_at（R62，5 分钟一次）与实时权限在同一事务里取齐；是否必须登录交给 RequireAuth
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

const sessionValiditySQL = `
	SELECT (revoked_at IS NOT NULL OR expires_at <= now())
	  FROM sessions
	 WHERE tenant_id = $1
	   AND id = $2::uuid
	   AND user_id = $3::uuid
	   AND audience = $4`

// sessionTouchSQL 是会话 last_seen_at 的唯一写入点（R62）：两个网关都没有刷新令牌接口，
// 认证中间件是每个带令牌的请求必经之处。节流到 5 分钟一次，大多数请求不写；
// 并发请求撞上同一行时，后到的在前一个提交后重判 WHERE 落空，不会连写两次。
const sessionTouchSQL = `
	UPDATE sessions SET last_seen_at = now()
	 WHERE tenant_id = $1
	   AND id = $2::uuid
	   AND last_seen_at < now() - interval '5 minutes'`

type authTransactionRunner interface {
	InTx(context.Context, db.Scope, func(pgx.Tx) error) error
}

// Authenticate 解析 Bearer 令牌并装配 Principal。
//
// 关键取舍：权限不放进令牌，而是每次请求实时查库。
// 放进令牌意味着「撤销某人权限后，他手里的令牌在过期前依然好使」——
// 对 IAM-010 的临时提权「到期后无需人工操作即可回收」是致命的。
// 代价是每请求一次索引查询，在权限表规模下可忽略。
//
// 无令牌不是错误：公开接口（套餐列表、状态页）需要匿名访问。
// 是否必须登录由 RequireAuth 决定，职责分离。
func Authenticate(pool authTransactionRunner, iss *token.Issuer, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := bearerToken(r)
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}

			claims, err := iss.Verify(raw)
			if err != nil {
				// 统一文案：不区分「过期」「签名错」「域不符」，
				// 否则调用方能据此推断令牌结构（SEC-006）
				log.Info("令牌校验失败",
					slog.String("reason", err.Error()),
					slog.String("request_id", httpx.RequestIDFrom(r.Context())))
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期"))
				return
			}
			if !validInteractiveClaims(claims, httpx.TenantIDFrom(r.Context())) {
				log.Info("token claims rejected",
					slog.String("request_id", httpx.RequestIDFrom(r.Context())))
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期"))
				return
			}

			ctx := r.Context()
			p := &httpx.Principal{
				Kind:             claims.Kind,
				Audience:         claims.Audience,
				UserID:           claims.Subject,
				TenantID:         claims.TenantID,
				SessionID:        claims.SessionID,
				DeviceID:         claims.DeviceID,
				AuthMethods:      claims.AuthMeth,
				ReauthedRecently: iss.ReauthedRecently(claims),
			}

			// 会话有效性 + 权限，一次事务内取齐
			scope := db.Scope{TenantID: claims.TenantID, ActorID: claims.Subject}
			var sessionRevoked bool

			err = pool.InTx(ctx, scope, func(tx pgx.Tx) error {
				{
					// IAM-005：用户远程注销后，未过期的访问令牌必须立即失效
					if err := tx.QueryRow(ctx, sessionValiditySQL,
						claims.TenantID, claims.SessionID, claims.Subject, claims.Audience,
					).Scan(&sessionRevoked); err != nil {
						if err == pgx.ErrNoRows {
							sessionRevoked = true
							return nil
						}
						return err
					}
					if sessionRevoked {
						return nil
					}
					if _, err := tx.Exec(ctx, sessionTouchSQL, claims.TenantID, claims.SessionID); err != nil {
						return err
					}
				}

				rows, err := tx.Query(ctx, `
					SELECT DISTINCT rp.permission_code
					  FROM role_bindings rb
					  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
					  JOIN role_permissions rp ON rp.role_id = rb.role_id
					 WHERE rb.tenant_id = $1
					   AND rb.user_id = $2
					   AND (rb.expires_at IS NULL OR rb.expires_at > now())
					   AND ($3::text <> 'admin' OR
					        (rb.scope_type = 'tenant' AND rb.scope_id IS NULL))`,
					claims.TenantID, claims.Subject, claims.Audience)
				if err != nil {
					return err
				}
				defer rows.Close()

				for rows.Next() {
					var code string
					if err := rows.Scan(&code); err != nil {
						return err
					}
					p.Permissions = append(p.Permissions, code)
				}
				return rows.Err()
			})

			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if sessionRevoked {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期"))
				return
			}

			next.ServeHTTP(w, r.WithContext(httpx.WithPrincipal(ctx, p)))
		})
	}
}

func validInteractiveClaims(claims *token.Claims, requestTenantID string) bool {
	if claims == nil || uuid.Validate(claims.Subject) != nil ||
		uuid.Validate(claims.TenantID) != nil || uuid.Validate(claims.SessionID) != nil {
		return false
	}
	if requestTenantID == "" || claims.TenantID != requestTenantID || claims.Kind != "user" {
		return false
	}
	return claims.Audience == "public" || claims.Audience == "admin"
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	// 大小写不敏感匹配 "Bearer "，但严格要求恰好一个空格分隔
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}
