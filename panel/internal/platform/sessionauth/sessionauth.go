// Package sessionauth 是认证中间件对数据库的那一次往返：会话有效性、last_seen_at 节流刷新与
// 后台权限展开合成一条语句。
//
// SQL 放在 platform 而不是 middleware：中间件不写 SQL（2026-10-07 定的分层规则），也不能 import
// domain（会话表的写入方 identity 在 domain 层）。本包只依赖 platform/db。
package sessionauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// validitySQL 取会话是否已失效（IAM-005：远程注销后未过期的访问令牌立即失效）。
// 它是 authSQL 里的第一个 CTE，结果列名 revoked。
const validitySQL = `
	SELECT (revoked_at IS NOT NULL OR expires_at <= now()) AS revoked
	  FROM sessions
	 WHERE tenant_id = $1
	   AND id = $2::uuid
	   AND user_id = $3::uuid
	   AND audience = $4`

// touchSQL 是会话 last_seen_at 的唯一写入点（R62）：两个网关都没有刷新令牌接口，
// 认证中间件是每个带令牌的请求必经之处。节流到 5 分钟一次，大多数请求不写；
// 并发请求撞上同一行时，后到的在前一个提交后重判 WHERE 落空，不会连写两次。
// 只刷新仍有效的会话（引用同一语句里的 sess CTE）。
const touchSQL = `
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

// authSQL 把会话有效性、节流刷新与后台权限展开合成一条语句。
//
// 以前是一个事务里 BEGIN、注入租户、查有效性、刷新、展开权限、COMMIT 六次往返，
// 外加归还连接时一次清理；现在经 QueryRowScoped 一次往返：
//   - touch 是数据修改 CTE，不被主查询引用也一定执行，和读取在同一个隐式事务里提交；
//   - 会话不存在时主查询无行（pgx.ErrNoRows），按失效处理；
//   - 门户（audience=public）不展开权限：门户没有任何路由按权限放行（没有
//     RequirePermission），GET v1/me 回的 permissions 前端也不读，CASE 不成立时
//     子查询不执行。
//
// 不做进程内缓存：吊销（远程注销、改密、停用）必须下一个请求立即生效，而三个网关之间
// 没有送达有保证的失效通道；权限也要实时（IAM-010 临时提权到期即回收）。
const authSQL = `
	WITH sess AS (` + validitySQL + `
	), touch AS (` + touchSQL + `
	)
	SELECT sess.revoked,
	       CASE WHEN $4 = 'admin' AND NOT sess.revoked
	            THEN ARRAY(` + adminPermissionsSQL + `)
	       END
	  FROM sess`

// Querier 是本包对数据库的全部要求：一次往返的单行查询（*db.Pool 的 QueryRowScoped）。
type Querier interface {
	QueryRowScoped(ctx context.Context, s db.Scope, sql string, args []any, dest ...any) error
}

// Session 是令牌里用来定位会话的四个字段。
type Session struct {
	TenantID, SessionID, UserID, Audience string
}

// Result 是一次认证查询的结果。Revoked 为真时 Permissions 恒为空。
type Result struct {
	Revoked     bool
	Permissions []string
}

// Check 在一次往返里确认会话仍有效、按 5 分钟节流刷新 last_seen_at，后台会话再展开实时权限。
// 会话行不存在（被删、属于别的用户或别的 audience）按已失效返回，不是错误。
func Check(ctx context.Context, q Querier, s Session) (Result, error) {
	var out Result
	var permissions []string
	err := q.QueryRowScoped(ctx, db.Scope{TenantID: s.TenantID, ActorID: s.UserID}, authSQL,
		[]any{s.TenantID, s.SessionID, s.UserID, s.Audience}, &out.Revoked, &permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{Revoked: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	if !out.Revoked && len(permissions) > 0 {
		out.Permissions = permissions
	}
	return out, nil
}
