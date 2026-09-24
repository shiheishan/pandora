package identity

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestPasswordRotationIsExactAndRevokesAllLoginCredentials(t *testing.T) {
	source, err := os.ReadFile("password.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, want := range []string{
		"SELECT phc",
		"FOR UPDATE",
		"passwordTag.RowsAffected() != 1",
		"revoked_reason = 'password_changed'",
		"UPDATE refresh_tokens",
		"status = 'revoked'",
		"credentialrevocation.RevokeRefreshFamilies",
		`Action:       "user.password_changed"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("password transaction contract is missing %q", want)
		}
	}
	passwordWrite := strings.Index(text, "passwordTag, err := tx.Exec")
	sessionRevoke := strings.Index(text, "UPDATE sessions")
	refreshRevoke := strings.Index(text, "UPDATE refresh_tokens")
	refreshFamilyRevoke := strings.Index(text, "credentialrevocation.RevokeRefreshFamilies")
	auditSuccess := strings.LastIndex(text, `return writeAudit("success", "")`)
	if passwordWrite < 0 || sessionRevoke <= passwordWrite || refreshRevoke <= sessionRevoke ||
		refreshFamilyRevoke <= refreshRevoke || auditSuccess <= refreshFamilyRevoke {
		t.Fatal("password, session, refresh-token, refresh-family and audit writes are not ordered in one transaction")
	}
}

// 管理员改自己密码至少 12 位（契约后台外壳）；门户仍是 8 位。错误键都是 password。
func TestValidatePasswordForDomain(t *testing.T) {
	for _, tc := range []struct {
		domain, password string
		ok               bool
	}{
		{"public", "abcd1234", true},
		{"admin", "abcd1234", false},
		{"admin", "abcdefgh1234", true},
		{"admin", "中文口令中文口令中文口1", true},
		{"admin", "abcdefghijkl", false},
	} {
		err := validatePasswordFor(tc.domain, tc.password)
		if (err == nil) != tc.ok {
			t.Errorf("validatePasswordFor(%s, %q) err=%v, want ok=%v", tc.domain, tc.password, err, tc.ok)
		}
		var he *httpx.Error
		if err != nil && (!errors.As(err, &he) || he.Fields["password"] == "") {
			t.Errorf("validatePasswordFor(%s, %q) err=%v, want fields.password", tc.domain, tc.password, err)
		}
	}
}
