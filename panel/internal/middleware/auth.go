package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// sessionValiditySQL 取会话是否已失效（IAM-005：远程注销后未过期的访问令牌立即失效）。
// 它是 sessionAuthSQL 里的第一个 CTE，结果列名 revoked。
const sessionValiditySQL = `
	SELECT (revoked_at IS NOT NULL OR expires_at <= now()) AS revoked
	  FROM sessions
	 WHERE tenant_id = $1
	   AND id = $2::uuid
	   AND user_id = $3::uuid
	   AND audience = $4`

// sessionTouchSQL 是会话 last_seen_at 的唯一写入点（R62）：两个网关都没有刷新令牌接口，
// 认证中间件是每个带令牌的请求必经之处。节流到 5 分钟一次，大多数请求不写；
// 并发请求撞上同一行时，后到的在前一个提交后重判 WHERE 落空，不会连写两次。
// 只刷新仍有效的会话（引用同一语句里的 sess CTE）。
const sessionTouchSQL = `
	UPDATE sessions SET last_seen_at = now()
	 WHERE tenant_id = $1
	   AND id = $2::uuid
	   AND last_seen_at < now() - interval '5 minutes'
	   AND EXISTS (SELECT 1 FROM sess WHERE NOT sess.revoked)`

// adminPermissionsSQL 展开后台主体当前生效的权限码（IAM-009 / IAM-010：过期的临时
// 提权不算数）。后台只认租户范围的绑定。
const adminPermissionsSQL = `
	SELECT DISTINCT rp.permission_code
	  FROM role_bindings rb
	  JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
	  JOIN role_permissions rp ON rp.role_id = rb.role_id
	 WHERE rb.tenant_id = $1
	   AND rb.user_id = $3::uuid
	   AND (rb.expires_at IS NULL OR rb.expires_at > now())
	   AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
	 ORDER BY rp.permission_code`

// sessionAuthSQL 把会话有效性、节流刷新与后台权限展开合成一条语句。
//
// 以前是一个事务里 BEGIN、注入租户、查有效性、刷新、展开权限、COMMIT 六次往返，
// 外加归还连接时一次清理；现在经 QueryRowScoped 一次往返：
//   - touch 是数据修改 CTE，不被主查询引用也一定执行，和读取在同一个隐式事务里提交；
//   - 会话不存在时主查询无行（pgx.ErrNoRows），按失效处理；
//   - 门户（audience=public）不展开权限：门户没有任何路由按权限放行（没有
//     RequirePermission），GET v1/me 回的 permissions 前端也不读，CASE 不成立时
//     子查询不执行。
const sessionAuthSQL = `
	WITH sess AS (` + sessionValiditySQL + `
	), touch AS (` + sessionTouchSQL + `
	)
	SELECT sess.revoked,
	       CASE WHEN $4 = 'admin' AND NOT sess.revoked
	            THEN ARRAY(` + adminPermissionsSQL + `)
	       END
	  FROM sess`

// authSessionQuerier 是认证中间件对数据库的全部要求：一次往返的单行查询
// （*db.Pool 的 QueryRowScoped）。
type authSessionQuerier interface {
	QueryRowScoped(ctx context.Context, s db.Scope, sql string, args []any, dest ...any) error
}

// Authenticate 解析 Bearer 令牌并装配 Principal。
//
// 关键取舍：权限不放进令牌，而是每次请求实时查库。
// 放进令牌意味着「撤销某人权限后，他手里的令牌在过期前依然好使」——
// 对 IAM-010 的临时提权「到期后无需人工操作即可回收」是致命的。
// 代价是每请求一次查询；会话有效性、节流刷新与权限展开合在这一次往返里（sessionAuthSQL）。
//
// 无令牌不是错误：公开接口（套餐列表、状态页）需要匿名访问。
// 是否必须登录由 RequireAuth 决定，职责分离。
func Authenticate(pool authSessionQuerier, iss *token.Issuer, log *slog.Logger) func(http.Handler) http.Handler {
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

			// 会话有效性 + 节流刷新 + 后台权限，一次往返取齐
			scope := db.Scope{TenantID: claims.TenantID, ActorID: claims.Subject}
			var sessionRevoked bool
			var permissions []string
			err = pool.QueryRowScoped(ctx, scope, sessionAuthSQL,
				[]any{claims.TenantID, claims.SessionID, claims.Subject, claims.Audience},
				&sessionRevoked, &permissions)
			if errors.Is(err, pgx.ErrNoRows) {
				sessionRevoked, err = true, nil
			}
			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if sessionRevoked {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期"))
				return
			}
			if len(permissions) > 0 {
				p.Permissions = permissions
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
