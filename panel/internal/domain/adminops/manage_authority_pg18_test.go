package adminops

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// seedStatusAuthority 建一个租户：super（平台级，多一项 billing.ledger.read）、
// ta1 / ta2（租户管理员，永久租户级）、plain（无角色），全部虚构。
func seedStatusAuthority(t *testing.T, ctx context.Context, admin *pgxpool.Pool, prefix, slug string) (tenant, super, ta1, ta2, plain, superRole string) {
	t.Helper()
	tenant, super, ta1, ta2, plain = prefix+"000000000001", prefix+"000000000011", prefix+"000000000012",
		prefix+"000000000013", prefix+"000000000014"
	superRole = prefix + "000000000021"
	adminRole := prefix + "000000000022"
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,$2,'Status Authority','CNY')`, []any{tenant, slug}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		    ($2,$1,'super@status-authority.invalid','Super','active'),
		    ($3,$1,'ta1@status-authority.invalid','TA1','active'),
		    ($4,$1,'ta2@status-authority.invalid','TA2','active'),
		    ($5,$1,'plain@status-authority.invalid','Plain','active')`, []any{tenant, super, ta1, ta2, plain}},
		{`SET LOCAL session_replication_role = replica`, nil},
		{`INSERT INTO roles(id,tenant_id,code,name) VALUES ($2,$1,'pg18_super','平台'),($3,$1,'pg18_admin','租户管理员')`,
			[]any{tenant, superRole, adminRole}},
		{`INSERT INTO role_permissions(role_id,permission_code)
		    SELECT $1::uuid, unnest(ARRAY['iam.user.read','iam.user.write','iam.role.read','iam.role.write','billing.ledger.read'])
		    UNION ALL
		    SELECT $2::uuid, unnest(ARRAY['iam.user.read','iam.user.write','iam.role.read','iam.role.write'])`,
			[]any{superRole, adminRole}},
		{`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type) VALUES
		    ($1,$2,$5,'tenant'),($1,$3,$6,'tenant'),($1,$4,$6,'tenant')`,
			[]any{tenant, super, ta1, ta2, superRole, adminRole}},
	} {
		if _, err := tx.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed status authority: %v\nSQL: %s", err, row.sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return
}

func TestSetUserStatusAuthorityPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	svc := NewService(app)
	tenant, super, ta1, ta2, plain, superRole := seedStatusAuthority(t, ctx, admin,
		"9c000000-0000-4000-8000-", "status-authority-pg18")
	statusOf := func(user string) string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT status FROM users WHERE id=$1`, user).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	wantCode := func(label string, err error, want httpx.Code) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != want {
			t.Fatalf("%s: err=%v, want %s", label, err, want)
		}
	}

	// 越级被拒：租户管理员停用 / 封禁平台级账号都是 403，状态不变；连「状态没变」的空操作也不放行
	wantCode("ta1 suspends super", svc.SetUserStatus(ctx, tenant, ta1, super, "suspended", "越级测试"), httpx.CodeForbidden)
	wantCode("ta1 no-op on super", svc.SetUserStatus(ctx, tenant, ta1, super, "active", ""), httpx.CodeForbidden)
	if statusOf(super) != "active" {
		t.Fatal("refused status change still applied")
	}
	// 同级、下级允许
	if err := svc.SetUserStatus(ctx, tenant, ta1, ta2, "suspended", "同级停用"); err != nil {
		t.Fatalf("same-level suspend: %v", err)
	}
	if err := svc.SetUserStatus(ctx, tenant, ta1, plain, "banned", "封禁普通用户"); err != nil {
		t.Fatalf("suspend ordinary user: %v", err)
	}
	if err := svc.SetUserStatus(ctx, tenant, super, ta2, "active", ""); err != nil {
		t.Fatalf("super restores tenant admin: %v", err)
	}
	if statusOf(ta2) != "active" || statusOf(plain) != "banned" {
		t.Fatalf("status after allowed changes ta2=%s plain=%s", statusOf(ta2), statusOf(plain))
	}

	// 最后一个管理员守卫不变：只剩 super 一个永久有效管理员时，权限覆盖它的临时管理员
	// （平台级角色、带到期时间，管得了但自己不算有效管理员）也停不掉它，回 409
	for _, sql := range []string{
		`DELETE FROM role_bindings WHERE user_id IN ('` + ta1 + `','` + ta2 + `')`,
		`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type,expires_at)
		   VALUES ('` + tenant + `','` + ta1 + `','` + superRole + `','tenant',now() + interval '1 hour')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("reshape bindings: %v", err)
		}
	}
	err := svc.SetUserStatus(ctx, tenant, ta1, super, "suspended", "最后一个管理员")
	if !errors.Is(err, iamguard.ErrLastEffectiveAdministrator) {
		t.Fatalf("suspending the last effective administrator: err=%v, want last-admin conflict", err)
	}
	if statusOf(super) != "active" {
		t.Fatal("last effective administrator was suspended")
	}
	t.Log("marker=set_user_status_authority_ok")
}
