package adminops

import (
	"os"
	"strings"
	"testing"
)

func TestSetUserStatusPreservesLastAdministratorBeforeAudit(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (s *Service) SetUserStatus")
	end := strings.Index(text[start:], "//==============================================================================\n// 订单")
	if start < 0 || end < 0 {
		t.Fatal("SetUserStatus source boundary not found")
	}
	body := text[start : start+end]
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
