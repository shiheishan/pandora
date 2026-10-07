package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sessionauth"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// authSessionQuerier 是认证中间件对数据库的全部要求：一次往返的单行查询
// （*db.Pool 的 QueryRowScoped）。语句本身在 platform/sessionauth（中间件不写 SQL）。
type authSessionQuerier = sessionauth.Querier

// Authenticate 解析 Bearer 令牌并装配 Principal。
//
// 关键取舍：权限不放进令牌，而是每次请求实时查库。
// 放进令牌意味着「撤销某人权限后，他手里的令牌在过期前依然好使」——
// 对 IAM-010 的临时提权「到期后无需人工操作即可回收」是致命的。
// 代价是每请求一次查询；会话有效性、节流刷新与权限展开合在这一次往返里（sessionauth.Check）。
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

			// 会话有效性 + 节流刷新 + 后台权限，一次往返取齐（sessionauth.Check）
			res, err := sessionauth.Check(ctx, pool, sessionauth.Session{TenantID: claims.TenantID,
				SessionID: claims.SessionID, UserID: claims.Subject, Audience: claims.Audience})
			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if res.Revoked {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized, "凭据无效或已过期"))
				return
			}
			p.Permissions = res.Permissions

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
