package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

// 改前的登录查口令 SQL：只用于对照结果与计划，不在生产代码里出现。
const legacyLoginCredentialSQL = `
			SELECT u.id, u.status, p.phc
			  FROM users u
			  JOIN user_passwords p ON p.user_id = u.id
			 WHERE u.tenant_id = $1 AND u.email = $2`

type planNode struct {
	NodeType     string     `json:"Node Type"`
	RelationName string     `json:"Relation Name"`
	IndexName    string     `json:"Index Name"`
	IndexCond    string     `json:"Index Cond"`
	Filter       string     `json:"Filter"`
	SharedHit    int64      `json:"Shared Hit Blocks"`
	SharedRead   int64      `json:"Shared Read Blocks"`
	Plans        []planNode `json:"Plans"`
}

type explained struct {
	Plan          planNode `json:"Plan"`
	ExecutionTime float64  `json:"Execution Time"`
}

// explainAsApp 以运行角色、在租户作用域里 EXPLAIN（RLS 生效），返回计划树。
func explainAsApp(ctx context.Context, t *testing.T, app *platformdb.Pool, tenantID, query string, args ...any) explained {
	t.Helper()
	var raw []byte
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query, args...).Scan(&raw)
	}); err != nil {
		t.Fatalf("explain: %v", err)
	}
	var out []explained
	if err := json.Unmarshal(raw, &out); err != nil || len(out) != 1 {
		t.Fatalf("decode plan: %v %s", err, raw)
	}
	return out[0]
}

// usesIndexCond 报告计划树里有没有节点用 index 走、且 Index Cond 含 cond
// （Index Scan 与 Bitmap Index Scan 都算）。
func usesIndexCond(n planNode, index, cond string) bool {
	if n.IndexName == index && strings.Contains(n.IndexCond, cond) {
		return true
	}
	for _, c := range n.Plans {
		if usesIndexCond(c, index, cond) {
			return true
		}
	}
	return false
}

func describePlan(n planNode, depth int, b *strings.Builder) {
	fmt.Fprintf(b, "%s%s rel=%s idx=%s cond=%q filter=%q hit=%d read=%d\n",
		strings.Repeat("  ", depth), n.NodeType, n.RelationName, n.IndexName, n.IndexCond, n.Filter, n.SharedHit, n.SharedRead)
	for _, c := range n.Plans {
		describePlan(c, depth+1, b)
	}
}

// 登录查口令在 RLS 下走 (tenant_id, email_lower) 索引，不再取出全租户的用户逐行比 citext；
// 结果与改前逐行一致（大小写不同的输入、存量混合大小写邮箱、不存在的邮箱）。
func TestLoginEmailLookupPG18(t *testing.T) {
	ctx, admin, app := openLogoutPG18Fixture(t)

	const (
		tenantID   = "c19e0000-0000-7000-8000-000000000001"
		mixedUser  = "c19e0000-0000-7000-8000-000000000011"
		mixedEmail = "Mixed.Case@Login-Lookup.example.test"
		phc        = "$argon2id$v=19$m=19456,t=2,p=1$bG9naW4tbG9va3Vw$bG9naW4tbG9va3VwLXBnMTg"
		population = 3000
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'login-lookup-pg18', 'Login Lookup PG18', 'USD')`, []any{tenantID}},
		{`INSERT INTO users (tenant_id, email, status)
		  SELECT $1, 'user' || g || '@login-lookup.example.test', 'active' FROM generate_series(1, $2::int) g`, []any{tenantID, population}},
		{`INSERT INTO user_passwords (user_id, tenant_id, phc) SELECT id, tenant_id, $2 FROM users WHERE tenant_id = $1`, []any{tenantID, phc}},
		{`INSERT INTO users (id, tenant_id, email, status) VALUES ($2, $1, $3, 'active')`, []any{tenantID, mixedUser, mixedEmail}},
		{`INSERT INTO user_passwords (user_id, tenant_id, phc) VALUES ($2, $1, $3)`, []any{tenantID, mixedUser, phc}},
		{`ANALYZE users`, nil},
		{`ANALYZE user_passwords`, nil},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed login lookup fixture: %v", err)
		}
	}

	// 生成列口径：lower(email::text)
	var stored string
	if err := admin.QueryRow(ctx, `SELECT email_lower FROM users WHERE id = $1`, mixedUser).Scan(&stored); err != nil ||
		stored != "mixed.case@login-lookup.example.test" {
		t.Fatalf("email_lower = %q, err=%v", stored, err)
	}

	lookup := func(query, email string) (string, error) {
		var id, status, gotPHC string
		err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, query, tenantID, email).Scan(&id, &status, &gotPHC)
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return id, err
	}
	for _, email := range []string{
		"mixed.case@login-lookup.example.test", // normalizeEmail 之后的形状
		"MIXED.CASE@LOGIN-LOOKUP.EXAMPLE.TEST", // 未归一的输入照样命中（citext 口径）
		"user42@login-lookup.example.test",
		"User2999@Login-Lookup.Example.Test",
		"nobody@login-lookup.example.test",
	} {
		got, err := lookup(loginCredentialSQL, email)
		if err != nil {
			t.Fatalf("lookup %q: %v", email, err)
		}
		want, err := lookup(legacyLoginCredentialSQL, email)
		if err != nil {
			t.Fatalf("legacy lookup %q: %v", email, err)
		}
		if got != want {
			t.Fatalf("lookup %q: new=%q legacy=%q", email, got, want)
		}
		if strings.HasPrefix(email, "nobody") != (got == "") {
			t.Fatalf("lookup %q found=%q", email, got)
		}
	}
	if got, _ := lookup(loginCredentialSQL, "MIXED.CASE@login-lookup.example.test"); got != mixedUser {
		t.Fatalf("mixed-case stored email not found: %q", got)
	}
	for email, want := range map[string]bool{
		"mixed.case@login-lookup.example.test": true,
		"USER7@login-lookup.example.test":      true,
		"fresh@login-lookup.example.test":      false,
	} {
		var taken bool
		if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, registrationEmailTakenSQL, tenantID, email).Scan(&taken)
		}); err != nil || taken != want {
			t.Fatalf("registration taken %q = %v, want %v (err=%v)", email, taken, want, err)
		}
	}

	// 计划：运行角色在 RLS 下按 email_lower 走索引
	const probe = "user1500@login-lookup.example.test"
	after := explainAsApp(ctx, t, app, tenantID, loginCredentialSQL, tenantID, probe)
	before := explainAsApp(ctx, t, app, tenantID, legacyLoginCredentialSQL, tenantID, probe)
	var afterTree, beforeTree strings.Builder
	describePlan(after.Plan, 0, &afterTree)
	describePlan(before.Plan, 0, &beforeTree)
	t.Logf("改前（citext 等号）%d 个用户：执行 %.3fms\n%s", population+1, before.ExecutionTime, beforeTree.String())
	t.Logf("改后（email_lower）%d 个用户：执行 %.3fms\n%s", population+1, after.ExecutionTime, afterTree.String())

	if !usesIndexCond(after.Plan, "idx_users_tenant_email_lower", "email_lower") {
		t.Fatalf("login lookup must use idx_users_tenant_email_lower with email_lower as an index condition:\n%s", afterTree.String())
	}
}
