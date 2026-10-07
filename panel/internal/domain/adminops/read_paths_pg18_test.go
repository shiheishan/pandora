package adminops

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/sessionauth"
)

// TestAdminReadPathsPG18 给 w5latency 的「每请求固定开销」出数：在 5000 个用户的库上，经一个只数
// 往返的 TCP 代理，逐项量后台与门户点名读路径的数据库部分（往返数、p50 耗时），并与改前的写法
// （InTx 包同样的语句）在同一环境对照。
//
// 往返按服务端发回的 ReadyForQuery（'Z'）计：扩展协议每个 Sync、简单协议每条语句各回一个，
// 正好是一次网络往返。连接池只开一条连接，量的是热路径（语句已缓存）。
//
// 断言只卡往返数（确定性的）；耗时只记日志，CI 机器与生产机型不同，供报告对照。
func TestAdminReadPathsPG18(t *testing.T) {
	ctx, admin, _ := pg18test.Open(t, pg18test.Fixture{
		Domain: "CATALOG_SALES", DatabasePrefix: "pandora_catalog_sales_",
		MarkerTable: "pandora_catalog_sales_test_marker", CommentTag: "pandora-catalog-sales-pg18",
	})
	const (
		tenant  = "5d1e0000-0000-4000-8000-000000000001"
		product = "5d1e0000-0000-4000-8000-000000000002"
		plan    = "5d1e0000-0000-4000-8000-000000000003"
		version = "5d1e0000-0000-4000-8000-000000000004"
		users   = 5000
	)
	userID := func(i int) string { return fmt.Sprintf("5d1e0000-0000-4000-8000-%012d", 100000+i) }
	seed := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','read-paths-pg18','Read Paths','CNY')`,
		`INSERT INTO products(id,tenant_id,code,name,status) VALUES('` + product + `','` + tenant + `','rp','RP','active')`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenant + `','` + product + `','rp','RP 月付','active')`,
		`INSERT INTO plan_versions(id,tenant_id,plan_id,version,max_devices) VALUES('` + version + `','` + tenant + `','` + plan + `',1,3)`,
		`INSERT INTO users(id,tenant_id,email,display_name,status,created_at)
		   SELECT ('5d1e0000-0000-4000-8000-' || lpad((100000+g)::text, 12, '0'))::uuid, '` + tenant + `',
		          'rp-user-' || g || '@read-paths.invalid', 'RP ' || g, 'active', now() - g * interval '1 minute'
		     FROM generate_series(0, ` + strconv.Itoa(users-1) + `) g`,
		`INSERT INTO subscriptions(tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,
		        current_period_start,current_period_end)
		   SELECT '` + tenant + `', u.id, '` + plan + `', '` + version + `',
		          CASE WHEN row_number() OVER (ORDER BY u.id) % 2 = 0 THEN 'active' ELSE 'expired' END,
		          'CNY', 3000, now() - interval '5 days',
		          CASE WHEN row_number() OVER (ORDER BY u.id) % 2 = 0 THEN now() + interval '25 days'
		               ELSE now() - interval '1 day' END
		     FROM users u WHERE u.tenant_id = '` + tenant + `'`,
		`INSERT INTO quota_balances(tenant_id,subscription_id,metric,period,period_start,period_end,granted,limit_value,consumed)
		   SELECT '` + tenant + `', s.id, 'traffic.bytes', 'cycle', now() - interval '5 days', now() + interval '25 days', 100, 100, 1
		     FROM subscriptions s WHERE s.tenant_id = '` + tenant + `'`,
		`INSERT INTO sessions(id,tenant_id,user_id,audience,auth_methods,expires_at,last_seen_at) VALUES
		   ('5d1e0000-0000-4000-8000-0000000000a1','` + tenant + `','` + userID(7) + `','public','{password}',now()+interval '1 day',now()),
		   ('5d1e0000-0000-4000-8000-0000000000a2','` + tenant + `','` + userID(8) + `','admin','{password}',now()+interval '1 day',now())`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	seed(`ANALYZE users, subscriptions, quota_balances, sessions`)

	proxy := newRoundTripProxy(t, os.Getenv("AEGIS_CATALOG_SALES_PG18_DSN"))
	app, err := db.OpenWithOptions(ctx, proxy.dsn, db.Options{MaxConns: 1, MinConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	svc := &Service{pool: app} // 不挂看板缓存：量的是直读
	scope := db.Scope{TenantID: tenant}

	type result struct {
		name string
		rt   int64
		p50  time.Duration
	}
	var results []result
	// measure 先跑两遍预热（准备语句、填语句缓存），再跑 15 遍取往返数与耗时中位数
	measure := func(name string, wantRT int64, fn func() error) result {
		t.Helper()
		for range 2 {
			if err := fn(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		var took []time.Duration
		var rt int64
		for range 15 {
			before := proxy.trips.Load()
			start := time.Now()
			if err := fn(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			took = append(took, time.Since(start))
			rt = proxy.trips.Load() - before
		}
		slices.Sort(took)
		r := result{name: name, rt: rt, p50: took[len(took)/2]}
		results = append(results, r)
		if wantRT > 0 && rt != wantRT {
			t.Errorf("%s: %d round trips, want %d", name, rt, wantRT)
		}
		return r
	}

	var one int
	measure("baseline SELECT 1 (QueryRowScoped)", 1, func() error {
		return app.QueryRowScoped(ctx, scope, `SELECT 1`, nil, &one)
	})
	measure("before: InTx + SELECT 1", 3, func() error {
		return app.InTx(ctx, scope, func(tx pgx.Tx) error { return tx.QueryRow(ctx, `SELECT 1`).Scan(&one) })
	})
	measure("auth sessionauth.Check (public)", 1, func() error {
		_, err := sessionauth.Check(ctx, app, sessionauth.Session{TenantID: tenant,
			SessionID: "5d1e0000-0000-4000-8000-0000000000a1", UserID: userID(7), Audience: "public"})
		return err
	})
	measure("auth sessionauth.Check (admin)", 1, func() error {
		_, err := sessionauth.Check(ctx, app, sessionauth.Session{TenantID: tenant,
			SessionID: "5d1e0000-0000-4000-8000-0000000000a2", UserID: userID(8), Audience: "admin"})
		return err
	})

	// 门户 /v1/me 的库部分：改前 InTx 包一条，改后同一条走一次往返（identity.PortalProfile 的两种写法）
	const portalProfileSQL = `SELECT email, display_name, status, created_at FROM users WHERE tenant_id = $1 AND id = $2`
	var email string
	var display *string
	var status string
	var created time.Time
	measure("portal me profile, before (InTx)", 3, func() error {
		return app.InTx(ctx, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, portalProfileSQL, tenant, userID(7)).Scan(&email, &display, &status, &created)
		})
	})
	measure("portal me profile, after (one batch)", 1, func() error {
		return app.QueryRowScoped(ctx, scope, portalProfileSQL, []any{tenant, userID(7)}, &email, &display, &status, &created)
	})

	measure("admin users/{id}, after (GetUser batch)", 1, func() error {
		_, err := svc.GetUser(ctx, tenant, userID(42))
		return err
	})
	measure("admin users list, after (count+page batch)", 1, func() error {
		_, _, err := svc.ListUsers(ctx, tenant, ListUsersInput{Limit: 25})
		return err
	})
	measure("admin users?q, after (count+page batch)", 1, func() error {
		_, _, err := svc.ListUsers(ctx, tenant, ListUsersInput{Query: "rp-user-42", Limit: 100})
		return err
	})
	legacyList := func(in ListUsersInput) func() error {
		return func() error {
			in, args, err := listUsersArgs(tenant, in)
			if err != nil {
				return err
			}
			return app.InTx(ctx, scope, func(tx pgx.Tx) error {
				var total int64
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM users u WHERE `+legacyListUsersWhereSQL, args...).Scan(&total); err != nil {
					return err
				}
				rows, err := tx.Query(ctx, legacyListUsersSQL, append(args, in.Limit, in.Offset)...)
				if err != nil {
					return err
				}
				_, err = scanUserRows(rows)
				return err
			})
		}
	}
	measure("admin users list, before (legacy SQL in InTx)", 4, legacyList(ListUsersInput{Limit: 25}))
	measure("admin users?q, before (legacy SQL in InTx)", 4, legacyList(ListUsersInput{Query: "rp-user-42", Limit: 100}))
	measure("admin overview, after (batch, uncached)", 1, func() error {
		_, err := svc.readOverview(ctx, tenant)
		return err
	})
	measure("admin system counts, after (batch)", 1, func() error {
		svc.SystemCounts(ctx, tenant, []string{"email", "telegram"})
		return nil
	})

	base := results[0].p50
	for _, r := range results {
		t.Logf("read path %-48s round_trips=%d p50=%s (≈ %.1f × baseline round trip)",
			r.name, r.rt, r.p50, float64(r.p50)/float64(base))
	}

	// 通用计划下的用户搜索与列表（pgx 缓存语句后的生产形态）
	cfg, err := pgxpool.ParseConfig(os.Getenv("AEGIS_CATALOG_SALES_PG18_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["plan_cache_mode"] = "force_generic_plan"
	cfg.MaxConns = 1
	gp, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer gp.Close()
	gsvc := &Service{pool: &db.Pool{Pool: gp}}
	for _, in := range []ListUsersInput{{Limit: 25}, {Query: "rp-user-42", Limit: 100}, {SubState: "active", Limit: 25}} {
		var took []time.Duration
		for range 15 {
			start := time.Now()
			if _, _, err := gsvc.ListUsers(ctx, tenant, in); err != nil {
				t.Fatalf("generic list %+v: %v", in, err)
			}
			took = append(took, time.Since(start))
		}
		slices.Sort(took)
		t.Logf("users list force_generic_plan %+v: p50=%s max=%s", in, took[len(took)/2], took[len(took)-1])
	}
	t.Log("marker=admin_read_paths_pg18_ok")
}

// roundTripProxy 是测试用的 TCP 代理：原样转发，顺带数服务端发回的 ReadyForQuery。
type roundTripProxy struct {
	dsn   string
	trips atomic.Int64
}

func newRoundTripProxy(t *testing.T, appDSN string) *roundTripProxy {
	t.Helper()
	cfg, err := pgx.ParseConfig(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	network, upstream := "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	if len(cfg.Host) > 0 && cfg.Host[0] == '/' {
		network, upstream = "unix", fmt.Sprintf("%s/.s.PGSQL.%d", cfg.Host, cfg.Port)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	p := &roundTripProxy{}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial(network, upstream)
			if err != nil {
				_ = client.Close()
				continue
			}
			go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
			go func() { p.pipeServer(client, server); _ = client.Close() }()
		}
	}()
	u := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password),
		Host: ln.Addr().String(), Path: "/" + cfg.Database, RawQuery: "sslmode=disable"}
	p.dsn = u.String()
	return p
}

// pipeServer 把服务端的字节流按消息（类型 1 字节 + 长度 4 字节 + 正文）转给客户端，数 'Z'。
// 连接串里 sslmode=disable，开头没有 SSLRequest 的单字节应答。
func (p *roundTripProxy) pipeServer(client, server net.Conn) {
	head := make([]byte, 5)
	for {
		if _, err := io.ReadFull(server, head); err != nil {
			return
		}
		if head[0] == 'Z' {
			p.trips.Add(1)
		}
		n := int(binary.BigEndian.Uint32(head[1:])) - 4
		if _, err := client.Write(head); err != nil {
			return
		}
		if n > 0 {
			if _, err := io.CopyN(client, server, int64(n)); err != nil {
				return
			}
		}
	}
}
