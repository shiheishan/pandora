package iamguard

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ErrTargetOutranksActor 是越级管理的拒绝：目标账号持有操作者没有的权限。
//
// 回 403 而不是 RequirePermission 那样的 404：能走到这里的操作者已经有
// iam.user.read / iam.user.write，目标账号在用户列表里本来就看得见，隐藏它的
// 存在没有意义；明确告诉操作者「这个人你管不了」，比一个莫名其妙的「不存在」好排查。
var ErrTargetOutranksActor = httpx.New(httpx.CodeForbidden,
	"目标账号持有你没有的权限，不能由你重置密码或变更状态")

// Authority 是操作者相对某个目标账号的管理资格。
type Authority struct {
	// Staff：目标账号有任何角色绑定（不论范围、不论是否过期）。口令策略与
	// 风控批量停用按它把后台人员与普通用户分开。
	Staff bool
	// Covered：操作者当前生效的租户级权限，覆盖目标账号当前生效的全部权限
	// （目标一侧不论绑定范围）。为 true 才允许操作者改目标的密码或状态。
	Covered bool
}

// staffBindingSQL 是「后台人员」的唯一定义：有任何角色绑定（不论范围、是否过期）。
// 租户是 $1，target 是目标账号所在的参数位置。
func staffBindingSQL(target string) string {
	return `SELECT 1 FROM role_bindings rb
	                WHERE rb.tenant_id = $1 AND rb.user_id = ` + target + `::uuid`
}

// 比较口径（越级检查选的是「权限集合包含」，不是角色级别）：
//
//   - 角色是可组合的权限集合，没有天然的高低序；「租户管理员」与「平台管理员」
//     的差别就体现在权限集合上。按集合包含判断，同级（集合相等）与下级（真子集）
//     允许，目标多出任何一项操作者没有的权限即拒绝。
//   - 操作者一侧只算租户范围、未过期的绑定，与认证中间件展开后台权限的口径一致
//     （后台只认这些）；目标一侧算全部未过期绑定、不论范围，宁可多算不少算。
//   - 普通用户（没有任何生效权限）任何持 iam.user.write 的操作者都能管。
//   - 在调用方事务里实时查库：不用请求开始时展开的权限快照，绑定在请求中途
//     到期或被收回时以库里此刻的状态为准。
var authoritySQL = `
	SELECT EXISTS (` + staffBindingSQL("$3") + `),
	       NOT EXISTS (
	         SELECT rp.permission_code
	           FROM role_bindings rb
	           JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
	           JOIN role_permissions rp ON rp.role_id = rb.role_id
	          WHERE rb.tenant_id = $1 AND rb.user_id = $3::uuid
	            AND (rb.expires_at IS NULL OR rb.expires_at > now())
	         EXCEPT
	         SELECT rp.permission_code
	           FROM role_bindings rb
	           JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
	           JOIN role_permissions rp ON rp.role_id = rb.role_id
	          WHERE rb.tenant_id = $1 AND rb.user_id = $2::uuid
	            AND (rb.expires_at IS NULL OR rb.expires_at > now())
	            AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL)`

// InspectTarget 在调用方事务里算出操作者对目标账号的管理资格。
func InspectTarget(ctx context.Context, tx pgx.Tx, tenantID, actorID, targetID string) (Authority, error) {
	var a Authority
	err := tx.QueryRow(ctx, authoritySQL, tenantID, actorID, targetID).Scan(&a.Staff, &a.Covered)
	return a, err
}

// CanManage 是改密、改状态这类「操作别人账号」的越级闸：操作者的权限必须覆盖
// 目标的全部权限，否则返回 ErrTargetOutranksActor。同时带回目标的 Authority，
// 调用方按 Staff 定口令策略（改密）或跳过后台人员（风控批量停用），不必再查一次。
//
// 它不替代路由上的 RequirePermission（有没有资格碰账号），也不替代最后一个
// 管理员守卫（改完之后是否还剩可登录的管理员）；三道各管一件事。
func CanManage(ctx context.Context, tx pgx.Tx, tenantID, actorID, targetID string) (Authority, error) {
	a, err := InspectTarget(ctx, tx, tenantID, actorID, targetID)
	if err != nil {
		return a, err
	}
	if !a.Covered {
		return a, ErrTargetOutranksActor
	}
	return a, nil
}

// IsStaff 报告账号是不是后台人员（定义见 staffBindingSQL）。管理员口令策略按它
// 判断：看的是账号本身，不是请求从哪个网关进来。
func IsStaff(ctx context.Context, tx pgx.Tx, tenantID, userID string) (bool, error) {
	var staff bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (`+staffBindingSQL("$2")+`)`, tenantID, userID).Scan(&staff)
	return staff, err
}
