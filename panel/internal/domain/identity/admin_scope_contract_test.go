// [INPUT]: 依赖 platform/sourcetest 按名取 Service.Login 的源码
// [OUTPUT]: 对外提供 TestAdminLoginPermissionExpansionRequiresTenantScope
// [POS]: identity 管理员登录展开权限时只取租户级、无作用域的角色绑定
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestAdminLoginPermissionExpansionRequiresTenantScope(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.Login")
	for _, want := range []string{"JOIN roles r", "$3::text <> 'admin'", "rb.scope_type = 'tenant'",
		"rb.scope_id IS NULL", "tenantID, userID, audience"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin login permission contract missing %q", want)
		}
	}
}
