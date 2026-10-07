package identity

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/token"
)

// 连接池归还时不再清理（db.OpenWithOptions 去掉了 AfterRelease）。这条用例用只有一条
// 连接的池，证明同一条后端连接被复用时，上一个事务 / 批次的租户与操作者不会留到下一个使用者：
// 事务外读到的都是空，RLS 表返回空集。三种注入路径都验：BEGIN 内联、参数化回退、单次往返批次。
func TestPooledTenantContextDoesNotLeakPG18(t *testing.T) {
	ctx, admin, _ := pg18test.Open(t, identityPG18Fixture)
	const (
		tenant = "9d000000-0000-4000-8000-000000000001"
		user   = "9d000000-0000-4000-8000-000000000011"
	)
	if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name,default_currency)
		VALUES($1,'pool-scope-pg18','Pool Scope','CNY')`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO users(id,tenant_id,email,display_name,status)
		VALUES($2,$1,'pool@scope.invalid','Pool','active')`, tenant, user); err != nil {
		t.Fatal(err)
	}
	// pg18test.Open 已核对过这是一次性库；这里用同一个 DSN 另开一个单连接的池
	single, err := platformdb.OpenWithOptions(ctx, strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_DSN")),
		platformdb.Options{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(single.Close)

	var pid int
	if err := single.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	// 事务外：同一条连接上看不到任何租户上下文，RLS 表为空
	assertClean := func(step string) {
		t.Helper()
		var gotPID, users int
		var tenantSetting, actorSetting string
		if err := single.QueryRow(ctx, `SELECT pg_backend_pid(),
			coalesce(current_setting('app.tenant_id', true), ''),
			coalesce(current_setting('app.actor_id', true), ''),
			(SELECT count(*) FROM users)`).Scan(&gotPID, &tenantSetting, &actorSetting, &users); err != nil {
			t.Fatalf("%s: inspect pooled connection: %v", step, err)
		}
		if gotPID != pid {
			t.Fatalf("%s: pool switched backend %d -> %d; the test no longer proves reuse", step, pid, gotPID)
		}
		if tenantSetting != "" || actorSetting != "" || users != 0 {
			t.Fatalf("%s: tenant context leaked tenant=%q actor=%q visible users=%d", step, tenantSetting, actorSetting, users)
		}
	}
	inside := func(label string, tx pgx.Tx, wantTenant string) {
		t.Helper()
		var gotPID, users int
		var tenantSetting string
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid(), current_setting('app.tenant_id'), (SELECT count(*) FROM users)`).
			Scan(&gotPID, &tenantSetting, &users); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if gotPID != pid || !strings.EqualFold(tenantSetting, wantTenant) || users != 1 {
			t.Fatalf("%s: pid=%d tenant=%q users=%d", label, gotPID, tenantSetting, users)
		}
	}
	assertClean("fresh")

	// 规范 UUID：BEGIN 与 set_config 合成一次往返
	if err := single.InTx(ctx, platformdb.Scope{TenantID: tenant, ActorID: user}, func(tx pgx.Tx) error {
		inside("inline scope", tx, tenant)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertClean("after inline InTx")

	// 非规范写法退回参数化注入；可串行化同理
	upper := strings.ToUpper(tenant)
	if err := single.InTxSerializable(ctx, platformdb.Scope{TenantID: upper}, func(tx pgx.Tx) error {
		inside("parameterized serializable scope", tx, upper)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertClean("after parameterized InTxSerializable")

	// 回滚的事务同样不留
	sentinel := errors.New("rollback")
	if err := single.InTx(ctx, platformdb.Scope{TenantID: tenant, ActorID: user}, func(tx pgx.Tx) error {
		inside("rolled back scope", tx, tenant)
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("rollback err=%v", err)
	}
	assertClean("after rollback")

	// 单次往返批次：租户对批内语句生效，批次结束即失效；批内写入与读取原子提交
	var tenantSetting string
	var users, gotPID int
	if err := single.QueryRowScoped(ctx, platformdb.Scope{TenantID: tenant, ActorID: user},
		`SELECT current_setting('app.tenant_id'), (SELECT count(*) FROM users), pg_backend_pid()`, nil,
		&tenantSetting, &users, &gotPID); err != nil || tenantSetting != tenant || users != 1 || gotPID != pid {
		t.Fatalf("scoped batch tenant=%q users=%d pid=%d err=%v", tenantSetting, users, gotPID, err)
	}
	assertClean("after scoped batch")

	var updated int
	if err := single.QueryRowScoped(ctx, platformdb.Scope{TenantID: tenant},
		`WITH w AS (UPDATE users SET display_name = 'batched' WHERE id = $1 RETURNING 1) SELECT count(*) FROM w`,
		[]any{user}, &updated); err != nil || updated != 1 {
		t.Fatalf("scoped batch write updated=%d err=%v", updated, err)
	}
	if err := single.QueryRowScoped(ctx, platformdb.Scope{TenantID: tenant},
		`WITH w AS (UPDATE users SET display_name = 'never' WHERE id = $1 RETURNING 1) SELECT 1/0 FROM w`,
		[]any{user}, &updated); err == nil {
		t.Fatal("division by zero inside the batch did not fail")
	}
	var name string
	if err := admin.QueryRow(ctx, `SELECT display_name FROM users WHERE id=$1`, user).Scan(&name); err != nil || name != "batched" {
		t.Fatalf("batch atomicity: display_name=%q err=%v, want the committed write only", name, err)
	}
	if err := single.QueryRowScoped(ctx, platformdb.Scope{TenantID: tenant},
		`SELECT 1 FROM users WHERE false`, nil, new(int)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("empty scoped result err=%v, want pgx.ErrNoRows", err)
	}
	assertClean("after scoped writes")
	t.Log("marker=pooled_tenant_context_isolated_ok")
}

// 认证中间件一次往返取齐会话有效性、节流刷新与后台权限：后台只展开租户级、未过期的绑定；
// 门户不展开；吊销的会话 401 且不刷新。
func TestAuthenticateSingleStatementPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, identityPG18Fixture)
	const (
		prefix  = "9e000000-0000-4000-8000-"
		tenant  = prefix + "000000000001"
		staff   = prefix + "000000000011"
		member  = prefix + "000000000012"
		adminSS = prefix + "000000000021"
		portSS  = prefix + "000000000022"
		roleA   = prefix + "000000000031"
		roleB   = prefix + "000000000032"
		roleC   = prefix + "000000000033"
	)
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','auth-one-pg18','Auth One','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		   ('` + staff + `','` + tenant + `','staff@auth-one.invalid','Staff','active'),
		   ('` + member + `','` + tenant + `','member@auth-one.invalid','Member','active')`,
		`INSERT INTO sessions(id,tenant_id,user_id,audience,auth_methods,expires_at,last_seen_at) VALUES
		   ('` + adminSS + `','` + tenant + `','` + staff + `','admin',ARRAY['password'],now()+interval '1 day',now()-interval '1 hour'),
		   ('` + portSS + `','` + tenant + `','` + member + `','public',ARRAY['password'],now()+interval '1 day',now()-interval '1 hour')`,
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO roles(id,tenant_id,code,name) VALUES
		   ('` + roleA + `','` + tenant + `','pg18_live','生效'),
		   ('` + roleB + `','` + tenant + `','pg18_lapsed','过期'),
		   ('` + roleC + `','` + tenant + `','pg18_self','非租户级')`,
		`INSERT INTO role_permissions(role_id,permission_code) VALUES
		   ('` + roleA + `','iam.user.read'),('` + roleA + `','node.read'),
		   ('` + roleB + `','billing.ledger.read'),('` + roleC + `','security.audit.read')`,
		`INSERT INTO role_bindings(tenant_id,user_id,role_id,scope_type,expires_at) VALUES
		   ('` + tenant + `','` + staff + `','` + roleA + `','tenant',NULL),
		   ('` + tenant + `','` + staff + `','` + roleB + `','tenant',now()-interval '1 day'),
		   ('` + tenant + `','` + staff + `','` + roleC + `','self',NULL),
		   ('` + tenant + `','` + member + `','` + roleA + `','tenant',NULL)`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	call := func(audience, user, session string) (int, *httpx.Principal) {
		t.Helper()
		issuer := token.NewIssuer(audience, []byte("auth-one-pg18-test-secret-32byte"), time.Hour)
		raw, err := issuer.Issue(token.Claims{Subject: user, TenantID: tenant, SessionID: session, Kind: "user"})
		if err != nil {
			t.Fatal(err)
		}
		var seen *httpx.Principal
		h := middleware.Authenticate(app, issuer, log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = httpx.PrincipalFrom(r.Context())
		}))
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil).WithContext(httpx.WithTenantID(ctx, tenant))
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, seen
	}
	lastSeen := func(session string) time.Time {
		t.Helper()
		var at time.Time
		if err := admin.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id=$1`, session).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	code, p := call("admin", staff, adminSS)
	if code != http.StatusOK || p == nil || strings.Join(p.Permissions, ",") != "iam.user.read,node.read" {
		t.Fatalf("admin principal status=%d permissions=%v, want only live tenant-scoped grants", code, p)
	}
	if time.Since(lastSeen(adminSS)) > time.Minute {
		t.Fatal("admin session was not touched in the same round trip")
	}
	// 门户主体不展开权限，哪怕账号本身有租户级角色
	code, p = call("public", member, portSS)
	if code != http.StatusOK || p == nil || p.Permissions != nil || time.Since(lastSeen(portSS)) > time.Minute {
		t.Fatalf("portal principal status=%d principal=%+v", code, p)
	}
	// 令牌拿去敲别的 audience 的会话：会话行对不上，401
	if code, _ := call("admin", member, portSS); code != http.StatusUnauthorized {
		t.Fatalf("admin token on a portal session: status=%d, want 401", code)
	}
	// 吊销后 401，且不刷新
	if _, err := admin.Exec(ctx, `UPDATE sessions SET revoked_at=now(), last_seen_at=now()-interval '1 hour' WHERE id=$1`, adminSS); err != nil {
		t.Fatal(err)
	}
	before := lastSeen(adminSS)
	if code, _ := call("admin", staff, adminSS); code != http.StatusUnauthorized || !lastSeen(adminSS).Equal(before) {
		t.Fatalf("revoked admin session: status=%d touched=%v", code, !lastSeen(adminSS).Equal(before))
	}
	t.Log("marker=authenticate_single_statement_ok")
}
