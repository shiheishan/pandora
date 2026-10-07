package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// identity 包的 PG18 用例都跑在 logout 域的一次性库里（run-pg18-gates.sh 的缺省过滤 PG18）。
var identityPG18Fixture = pg18test.Fixture{
	Domain: "LOGOUT", DatabasePrefix: "pandora_logout_",
	MarkerTable: "pandora_logout_test_marker", CommentTag: "pandora-logout-pg18",
}

// 越级用例的夹具：一个租户、三种角色、六个账号，全部虚构。
//
//	super    ：平台级角色，含租户管理员没有的 billing.ledger.read
//	ta1, ta2 ：租户管理员角色（iam.user.* / iam.role.*，永久租户级，都是有效管理员）
//	plain    ：没有任何角色
//	scoped   ：平台级角色但绑定范围是 self（目标一侧不论范围都算）
//	expired  ：平台级角色的绑定已过期（不算权限，但仍是后台人员）
type authorityFixture struct {
	tenant, super, ta1, ta2, plain, scoped, expired string
}

const authorityPassword = "Origin-pass-2026"

func seedAuthorityFixture(t *testing.T, ctx context.Context, admin *pgxpool.Pool, prefix, slug string) authorityFixture {
	t.Helper()
	f := authorityFixture{
		tenant: prefix + "000000000001", super: prefix + "000000000011", ta1: prefix + "000000000012",
		ta2: prefix + "000000000013", plain: prefix + "000000000014", scoped: prefix + "000000000015",
		expired: prefix + "000000000016",
	}
	superRole, adminRole := prefix+"000000000021", prefix+"000000000022"
	phc, err := crypto.HashPassword(authorityPassword, crypto.DefaultArgon2Params())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,$2,'Authority PG18','CNY')`, []any{f.tenant, slug}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		    ($2,$1,'super@authority.invalid','Super','active'),
		    ($3,$1,'ta1@authority.invalid','TA1','active'),
		    ($4,$1,'ta2@authority.invalid','TA2','active'),
		    ($5,$1,'plain@authority.invalid','Plain','active'),
		    ($6,$1,'scoped@authority.invalid','Scoped','active'),
		    ($7,$1,'expired@authority.invalid','Expired','active')`,
			[]any{f.tenant, f.super, f.ta1, f.ta2, f.plain, f.scoped, f.expired}},
		{`INSERT INTO user_passwords(user_id,tenant_id,phc)
		    SELECT id, tenant_id, $2 FROM users WHERE tenant_id = $1`, []any{f.tenant, phc}},
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO roles(id,tenant_id,code,name) VALUES ($2,$1,'pg18_super','平台'),($3,$1,'pg18_admin','租户管理员')`,
			[]any{f.tenant, superRole, adminRole}},
		{`INSERT INTO role_permissions(role_id,permission_code)
		    SELECT $1::uuid, unnest(ARRAY['iam.user.read','iam.user.write','iam.role.read','iam.role.write','node.read','billing.ledger.read'])
		    UNION ALL
		    SELECT $2::uuid, unnest(ARRAY['iam.user.read','iam.user.write','iam.role.read','iam.role.write','node.read'])`,
			[]any{superRole, adminRole}},
		{`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type,expires_at) VALUES
		    ($1,$2,$7,'tenant',NULL),
		    ($1,$3,$8,'tenant',NULL),
		    ($1,$4,$8,'tenant',NULL),
		    ($1,$5,$7,'self',NULL),
		    ($1,$6,$7,'tenant',now() - interval '1 day')`,
			[]any{f.tenant, f.super, f.ta1, f.ta2, f.scoped, f.expired, superRole, adminRole}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed authority fixture: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func wantHTTPCode(t *testing.T, label string, err error, want httpx.Code) {
	t.Helper()
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != want {
		t.Fatalf("%s: err=%v, want %s", label, err, want)
	}
}

func TestAdminResetPasswordAuthorityPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, identityPG18Fixture)
	f := seedAuthorityFixture(t, ctx, admin, "9a000000-0000-4000-8000-", "authority-reset-pg18")
	svc := NewService(app, nil, time.Hour, []byte("authority-pg18-salt"), false)
	phcOf := func(user string) string {
		t.Helper()
		var phc string
		if err := admin.QueryRow(ctx, `SELECT phc FROM user_passwords WHERE user_id=$1`, user).Scan(&phc); err != nil {
			t.Fatal(err)
		}
		return phc
	}
	reset := func(actor, target, password string) error {
		return svc.AdminResetPassword(ctx, f.tenant, AdminResetPasswordInput{
			TargetUserID: target, ActorID: actor, NewPassword: password, APIDomain: "admin",
		})
	}

	// 越级被拒：租户管理员重置平台级账号（含 self 范围的绑定）一律 403，口令不变、不留成功审计
	superBefore, scopedBefore := phcOf(f.super), phcOf(f.scoped)
	wantHTTPCode(t, "ta1 resets super", reset(f.ta1, f.super, "Takeover-pass-2026"), httpx.CodeForbidden)
	wantHTTPCode(t, "ta1 resets self-scoped super", reset(f.ta1, f.scoped, "Takeover-pass-2026"), httpx.CodeForbidden)
	if phcOf(f.super) != superBefore || phcOf(f.scoped) != scopedBefore {
		t.Fatal("refused reset still rotated the target password")
	}
	var leaked int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE tenant_id=$1 AND action='user.password_reset_by_admin' AND resource_id IN ($2,$3)`,
		f.tenant, f.super, f.scoped).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("refused reset audited as success: count=%d err=%v", leaked, err)
	}

	// 同级允许，但目标是后台人员：口令按账号至少 12 位
	wantHTTPCode(t, "ta1 resets ta2 with 8 chars", reset(f.ta1, f.ta2, "abcd1234"), httpx.CodeValidationFailed)
	if err := reset(f.ta1, f.ta2, "Peer-reset-2026"); err != nil {
		t.Fatalf("same-level reset: %v", err)
	}
	// 下级（普通用户）允许，8 位即可
	if err := reset(f.ta1, f.plain, "abcd1234"); err != nil {
		t.Fatalf("reset ordinary user: %v", err)
	}
	// 绑定已过期：权限不算（管得了），但仍是后台人员（12 位）
	wantHTTPCode(t, "ta1 resets expired staff with 8 chars", reset(f.ta1, f.expired, "abcd1234"), httpx.CodeValidationFailed)
	if err := reset(f.ta1, f.expired, "Lapsed-staff-2026"); err != nil {
		t.Fatalf("reset staff with only an expired binding: %v", err)
	}
	// 上级管下级
	if err := reset(f.super, f.ta1, "Super-reset-2026"); err != nil {
		t.Fatalf("super resets tenant admin: %v", err)
	}
	if ok, _, err := crypto.VerifyPassword("Peer-reset-2026", phcOf(f.ta2)); !ok || err != nil {
		t.Fatalf("same-level reset did not store the new password: ok=%v err=%v", ok, err)
	}
	t.Log("marker=admin_reset_password_authority_ok")
}

func TestChangePasswordPolicyAndFailureLimitPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, identityPG18Fixture)
	f := seedAuthorityFixture(t, ctx, admin, "9b000000-0000-4000-8000-", "authority-change-pg18")
	svc := NewService(app, nil, time.Hour, []byte("authority-pg18-salt"), false)
	change := func(user, old, next string) error {
		return svc.ChangePassword(ctx, f.tenant, ChangePasswordInput{
			UserID: user, OldPassword: old, NewPassword: next, APIDomain: "public",
		})
	}

	// 后台人员从门户改密，同样至少 12 位（以前门户只要 8 位）
	wantHTTPCode(t, "staff via portal with 8 chars", change(f.ta2, authorityPassword, "abcd1234"), httpx.CodeValidationFailed)
	if err := change(f.ta2, authorityPassword, "Portal-change-2026"); err != nil {
		t.Fatalf("staff 12+ char change: %v", err)
	}
	// 普通用户 8 位即可
	if err := change(f.plain, authorityPassword, "abcd1234"); err != nil {
		t.Fatalf("ordinary 8 char change: %v", err)
	}

	// 旧口令错满 5 次后，连正确的旧口令也 429，直到窗口滑过
	for i := 0; i < passwordChangeFailureLimit; i++ {
		wantHTTPCode(t, "wrong old password", change(f.plain, "wrong-old-1", "efgh5678"), httpx.CodeUnauthorized)
	}
	wantHTTPCode(t, "locked out", change(f.plain, "abcd1234", "efgh5678"), httpx.CodeRateLimited)
	var failures int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE tenant_id=$1 AND actor_id=$2 AND action='user.password_changed'
		  AND outcome='failure' AND error_code='invalid_password'`, f.tenant, f.plain).Scan(&failures); err != nil || failures != passwordChangeFailureLimit {
		t.Fatalf("failed attempts audited=%d err=%v, want %d", failures, err, passwordChangeFailureLimit)
	}
	// 窗口滑过后恢复（把失败审计的时间挪到窗口外：只在一次性库里，经超级用户绕过追加写守卫）
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`UPDATE audit_events SET occurred_at = occurred_at - interval '16 minutes'
		  WHERE tenant_id='` + f.tenant + `' AND actor_id='` + f.plain + `' AND error_code='invalid_password'`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("age failures: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := change(f.plain, "abcd1234", "efgh5678"); err != nil {
		t.Fatalf("change after the failure window: %v", err)
	}
	t.Log("marker=change_password_policy_and_limit_ok")
}
