package identity

import (
	"os"
	"strings"
	"testing"
)

func TestAdminLoginPermissionExpansionRequiresTenantScope(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "func (s *Service) Login")
	end := strings.Index(text[start:], "func normalizeEmail")
	if start < 0 || end < 0 {
		t.Fatal("Login source boundary not found")
	}
	body := text[start : start+end]
	for _, want := range []string{"JOIN roles r", "$3::text <> 'admin'", "rb.scope_type = 'tenant'",
		"rb.scope_id IS NULL", "tenantID, userID, audience"} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin login permission contract missing %q", want)
		}
	}
}
