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
