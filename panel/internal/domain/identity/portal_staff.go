package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 持有后台角色的账号不能登录门户（用户 2026-10-07 定）。
//
// 「持有后台角色」= 有任何未过期的角色绑定，不论范围。过期的临时提权不算：那个人已经
// 不是管理员了，再把他挡在门户外面只会让他哪儿都进不去。口令策略用的 iamguard.IsStaff
// 连过期绑定也算，那是「宁可多要求几位」，这里是「拦不拦」，口径刻意不同。
//
// 拦截点是门户签发会话的两处：口令登录（Login，audience=public）与快捷登录换会话
// （ConsumeQuickLogin）。口令登录只在口令校验通过之后才回这条提示：口令不对时照旧是
// 「邮箱或密码不正确」，不让人借这条提示枚举出哪些邮箱是管理员（IAM-006）。
// 提示只说「去管理后台的登录入口」，不带后台路径前缀（仓库与响应里都不出现部署值）。

// ErrStaffPortalLogin 是后台人员登录门户时的中性提示。
var ErrStaffPortalLogin = httpx.New(httpx.CodeForbidden,
	"该账号是管理账号，不能登录用户门户，请从管理后台的登录入口登录")

// portalStaffSQL 是「持有后台角色」的 SQL 片段，u 是 users 的别名。
const portalStaffSQL = `EXISTS (
	SELECT 1 FROM role_bindings rb
	 WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id
	   AND (rb.expires_at IS NULL OR rb.expires_at > now()))`

// holdsAdminRole 在调用方事务里判断账号是否持有后台角色（口径见 portalStaffSQL）。
func holdsAdminRole(ctx context.Context, tx pgx.Tx, tenantID, userID string) (bool, error) {
	var staff bool
	err := tx.QueryRow(ctx, `
		SELECT `+portalStaffSQL+`
		  FROM users u
		 WHERE u.tenant_id = $1 AND u.id = $2::uuid`, tenantID, userID).Scan(&staff)
	return staff, err
}
