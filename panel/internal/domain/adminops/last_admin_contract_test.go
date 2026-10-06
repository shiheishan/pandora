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
