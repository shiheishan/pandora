package identity

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestPasswordRotationIsExactAndRevokesAllLoginCredentials(t *testing.T) {
	text := sourcetest.Load(t, ".").Decl("Service.ChangePassword")
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

// 后台人员（有角色绑定）的口令至少 12 位，普通用户 8 位；看账号不看入口。错误键都是 password。
func TestValidatePasswordForAccount(t *testing.T) {
	for _, tc := range []struct {
		staff    bool
		password string
		ok       bool
	}{
		{false, "abcd1234", true},
		{true, "abcd1234", false},
		{true, "abcdefgh1234", true},
		{true, "中文口令中文口令中文口1", true},
		{true, "abcdefghijkl", false},
		{false, "abcdefgh", false},
	} {
		err := validatePasswordFor(tc.staff, tc.password)
		if (err == nil) != tc.ok {
			t.Errorf("validatePasswordFor(staff=%v, %q) err=%v, want ok=%v", tc.staff, tc.password, err, tc.ok)
		}
		var he *httpx.Error
		if err != nil && (!errors.As(err, &he) || he.Fields["password"] == "") {
			t.Errorf("validatePasswordFor(staff=%v, %q) err=%v, want fields.password", tc.staff, tc.password, err)
		}
	}
}

// 口令策略的 staff 判定来自账号本身：改密查 iamguard.IsStaff，后台重置用越级检查带回的
// Authority.Staff；两处都不再看 APIDomain。
func TestPasswordPolicyFollowsTargetAccountNotGateway(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	change := pkg.Decl("Service.ChangePassword")
	if !strings.Contains(change, "iamguard.IsStaff(ctx, tx, tenantID, userID)") ||
		!strings.Contains(change, "validatePasswordFor(staff, in.NewPassword)") {
		t.Fatal("ChangePassword must derive the password policy from the account's staff status")
	}
	reset := pkg.Decl("Service.AdminResetPassword")
	check := strings.Index(reset, "iamguard.CanManage(ctx, tx, tenantID, actor, target)")
	policy := strings.Index(reset, "validatePasswordFor(authority.Staff, in.NewPassword)")
	hash := strings.Index(reset, "slot.Hash(")
	write := strings.Index(reset, "INSERT INTO user_passwords")
	if check < 0 || policy < check || hash < policy || write < hash {
		t.Fatalf("AdminResetPassword must check authority, then policy, then hash and write: check=%d policy=%d hash=%d write=%d",
			check, policy, hash, write)
	}
	for _, decl := range []string{change, reset} {
		if strings.Contains(decl, "validatePasswordFor(apiDomain") || strings.Contains(decl, "validatePasswordFor(in.APIDomain") {
			t.Fatal("password policy must not depend on the gateway")
		}
	}
}
