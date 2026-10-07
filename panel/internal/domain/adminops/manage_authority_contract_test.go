package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 越级闸（iamguard.CanManage）在锁住目标之后、改状态之前；最后一个管理员守卫仍在写入之后、审计之前。
func TestSetUserStatusChecksAuthorityBeforeWriting(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.SetUserStatus")
	target := strings.Index(body, "SELECT status FROM users")
	check := strings.Index(body, "iamguard.CanManage(ctx, tx, tenantID, actorID, userID)")
	noop := strings.Index(body, "if before == status")
	update := strings.Index(body, "UPDATE users SET status")
	if !(target >= 0 && target < check && check < noop && noop < update) {
		t.Fatalf("authority ordering target=%d check=%d noop=%d update=%d", target, check, noop, update)
	}
}

// 风控批量停用：越级或后台人员一律跳过（SkipAdministrator），只停普通用户；判定来自 iamguard。
func TestDisableIPClusterAccountsSkipsStaffViaSharedGuard(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.DisableIPClusterAccounts")
	check := strings.Index(body, "iamguard.CanManage(ctx, tx, tenantID, actorID, id)")
	skip := strings.Index(body, "skip(id, SkipAdministrator)")
	update := strings.Index(body, "UPDATE users SET status = 'suspended'")
	if !(check >= 0 && check < skip && skip < update) {
		t.Fatalf("cluster disable ordering check=%d skip=%d update=%d", check, skip, update)
	}
	if !strings.Contains(body, "errors.Is(err, iamguard.ErrTargetOutranksActor) || (err == nil && authority.Staff)") {
		t.Fatal("cluster disable must skip both outranking targets and every staff account")
	}
	if strings.Contains(body, "FROM role_bindings") {
		t.Fatal("staff definition must come from iamguard, not an inline role_bindings query")
	}
}
