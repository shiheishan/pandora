package identity

import (
	"os"
	"strings"
	"testing"
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
