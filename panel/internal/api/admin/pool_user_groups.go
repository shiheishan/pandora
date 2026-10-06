// [INPUT]: 依赖 platform/httpx 的主体与错误、google/uuid
// [OUTPUT]: 对外提供 handlers 内部用的 namedRef（usergroup.go 也用）、requirePoolGroupsReauth、normalizePoolUserGroupIDs
// [POS]: api/admin 节点分组的「仅限用户组」名单（R104）：pools.go 的新建 / 编辑在请求带了 allowed_user_group_ids 时经这里做字段级 reauth 与格式校验；租户内存在性、整体替换与审计在 nodefabric 的 node_pool_user_groups.go，下发规则本身在 nodefabric.PoolAdmitsUserSQL
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 节点池的用户组限定名单。
//
// 名单决定「谁能连这组节点」，改它等于改交付集合：漏一个组就是一批用户
// 断网，多一个组就是有人连上了专属线路。所以三件事都比改池名严格：
// 带了这个字段就要近期重认证（路由上的 reauth 中间件只能整条挂，这里按
// 字段挂，不带字段的改名照旧不用重认证）、写一条前后对照的审计、名单真的
// 变了才在提交后通知节点重拉用户。

import (
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// namedRef 是列表里互相引用的 { id, name }：池的 allowed_user_groups、组的 exclusive_pools。
type namedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// maxPoolUserGroups 是一个池的名单上限。名单是「少数专属组」，上百个组的
// 限定说明该用可见性而不是线路来分，也免得一次请求拼出过大的数组。
const maxPoolUserGroups = 100

// requirePoolGroupsReauth 在请求带了 allowed_user_group_ids 时要求近期重认证，
// 与 middleware.RequireRecentReauth 同一个错误码与文案，前端的常驻对话框
// 据此弹框后用原请求重放。
func requirePoolGroupsReauth(r *http.Request, present bool) error {
	if !present {
		return nil
	}
	if p := httpx.PrincipalFrom(r.Context()); p == nil || !p.ReauthedRecently {
		return httpx.New(httpx.CodeReauthRequired, "此操作需要重新验证身份")
	}
	return nil
}

// normalizePoolUserGroupIDs 校验格式与重复并排序；存在性与租户归属由 nodefabric 在事务里查。
func normalizePoolUserGroupIDs(ids []string) ([]string, error) {
	invalid := func(msg string) error {
		return httpx.Invalid(map[string]string{"allowed_user_group_ids": msg})
	}
	if len(ids) > maxPoolUserGroups {
		return nil, invalid("一个节点池最多限定 100 个用户组")
	}
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, invalid("必须是无重复的用户组 UUID 列表")
		}
		out = append(out, id.String())
	}
	slices.Sort(out)
	if len(slices.Compact(slices.Clone(out))) != len(out) {
		return nil, invalid("必须是无重复的用户组 UUID 列表")
	}
	return out, nil
}
