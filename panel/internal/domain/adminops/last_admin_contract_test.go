// [INPUT]: 依赖 platform/sourcetest 按名取 Service.SetUserStatus 与 revokeUserLogins 的源码
// [OUTPUT]: 对外提供 TestSetUserStatusPreservesLastAdministratorBeforeAudit
// [POS]: adminops 停用用户时保住最后一个管理员，锁、改状态、复核、审计的次序
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestSetUserStatusPreservesLastAdministratorBeforeAudit(t *testing.T) {
	body := sourcetest.Load(t, ".").Decls("Service.SetUserStatus", "revokeUserLogins")
	lock := strings.Index(body, "iamguard.LockLastAdministrator")
	target := strings.Index(body, "SELECT status FROM users")
	update := strings.Index(body, "UPDATE users SET status")
	assert := strings.LastIndex(body, "iamguard.RequireEffectiveAdministrator")
	audit := strings.Index(body, "audit.Write")
	if !(lock >= 0 && lock < target && target < update && update < assert && assert < audit) {
		t.Fatalf("last-admin ordering lock=%d target=%d update=%d assert=%d audit=%d",
			lock, target, update, assert, audit)
	}
}
