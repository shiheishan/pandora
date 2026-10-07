package adminops

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestGenerateUsersPG18 钉住批量生成账号把 Argon2 挪到事务外之后的行为：账号、口令哈希、
// 分组、已验证邮箱与审计都在；返回的明文口令能验过库里的哈希；撞了已有邮箱的那个换后缀
// 重试，别的不受影响。
func TestGenerateUsersPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant = "7b000000-0000-4000-8000-000000000001"
		group  = "7b000000-0000-4000-8000-000000000002"
		actor  = "7b000000-0000-4000-8000-000000000011"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','gen-users-pg18','Gen Users','CNY')`,
		`INSERT INTO user_groups(id,tenant_id,code,name) VALUES('` + group + `','` + tenant + `','dealer','经销商')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('` + actor + `','` + tenant + `','ops@gen.invalid','Ops','active'),
		   (gen_random_uuid(),'` + tenant + `','gen-aaaaaaaa@gen.invalid','Taken','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}

	svc := NewService(app)
	got, err := svc.GenerateUsers(ctx, tenant, GenerateUsersInput{Count: 3, EmailPrefix: "Gen", EmailDomain: "GEN.invalid",
		GroupID: group, ActorID: actor, Reason: "给经销商预制账号"})
	if err != nil || len(got) != 3 {
		t.Fatalf("generate users = %+v err=%v", got, err)
	}
	seen := map[string]bool{}
	for _, u := range got {
		if !strings.HasPrefix(u.Email, "gen-") || !strings.HasSuffix(u.Email, "@gen.invalid") || seen[u.Email] {
			t.Fatalf("generated email %q", u.Email)
		}
		seen[u.Email] = true
		var phc, groupID string
		var verified bool
		if err := admin.QueryRow(ctx, `
			SELECT p.phc, u.user_group_id::text, u.email_verified_at IS NOT NULL
			  FROM users u JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`, tenant, u.Email).Scan(&phc, &groupID, &verified); err != nil {
			t.Fatalf("read generated user %s: %v", u.Email, err)
		}
		if ok, _, err := crypto.VerifyPassword(u.Password, phc); err != nil || !ok || groupID != group || !verified {
			t.Fatalf("generated user %s: password ok=%v err=%v group=%s verified=%v", u.Email, ok, err, groupID, verified)
		}
	}
	var audits int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'user.bulk_generated'`,
		tenant).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("bulk generation audit events=%d err=%v", audits, err)
	}

	// 撞邮箱：第一份口令先拿到已被占用的后缀，换后缀后照样写入；口令与账号一一对应
	suffixes := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"}
	next := func() (string, error) {
		s := suffixes[0]
		suffixes = suffixes[1:]
		return s, nil
	}
	creds := []generatedCredential{{password: "first-password-1A"}, {password: "second-password-2B"}}
	for i := range creds {
		if creds[i].phc, err = crypto.HashPassword(creds[i].password, crypto.DefaultArgon2Params()); err != nil {
			t.Fatal(err)
		}
	}
	var users []GeneratedUser
	if err := app.InTx(ctx, db.Scope{TenantID: tenant, ActorID: actor}, func(tx pgx.Tx) error {
		users, err = insertGeneratedUsers(ctx, tx, tenant, nil, "gen", "gen.invalid", creds, next)
		return err
	}); err != nil {
		t.Fatalf("insert with a colliding suffix: %v", err)
	}
	if len(users) != 2 || users[0].Email != "gen-cccccccc@gen.invalid" || users[1].Email != "gen-bbbbbbbb@gen.invalid" {
		t.Fatalf("collision retry emails = %+v", users)
	}
	for i, u := range users {
		var phc string
		if err := admin.QueryRow(ctx, `
			SELECT p.phc FROM users u JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`, tenant, u.Email).Scan(&phc); err != nil {
			t.Fatal(err)
		}
		if phc != creds[i].phc || u.Password != creds[i].password {
			t.Fatalf("user %s got password row of another credential", u.Email)
		}
	}
	t.Log("marker=generate_users_hash_outside_tx_ok")
}
