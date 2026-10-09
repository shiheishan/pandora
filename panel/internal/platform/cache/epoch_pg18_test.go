package cache

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// TestCacheEpochPG18 钉住 00154–00156 的纪元触发器与 platform/cache 的两条读法（w12cache）：
//
//   - 每张输入表的写在提交后推进对应的纪元序列，并在通道上发对应的载荷；Watch 收到通知的延迟
//     （与 w10quiet 的 watch_delivery 用例同一种写法：轮询到生效为止，超过 2 秒即红，日志打实测延迟）；
//   - 不该推进的写不推进：例行心跳、库存计数、updated_at，以及节点目录的变化不惊动下发纪元
//     （否则 aegis-node 的用户名单会跟着节点编辑重算）；
//   - 缓存按 Freshness 判有效：监听健康时命中零往返、写一提交就失效；监听停掉后退回读纪元，
//     写一提交照样失效、没写照样命中。
func TestCacheEpochPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CACHE_EPOCH", DatabasePrefix: "pandora_cache_epoch_",
		MarkerTable: "pandora_cache_epoch_test_marker", CommentTag: "pandora-cache-epoch-pg18",
	})
	const (
		tenant  = "cace0000-0000-4000-8000-000000000001"
		user    = "cace0000-0000-4000-8000-000000000011"
		product = "cace0000-0000-4000-8000-000000000021"
		plan    = "cace0000-0000-4000-8000-000000000031"
		pool    = "cace0000-0000-4000-8000-000000000041"
		server  = "cace0000-0000-4000-8000-000000000051"
		node    = "cace0000-0000-4000-8000-000000000061"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'cache-epoch-pg18','Cache Epoch PG18','CNY')`, tenant)
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'cache-epoch@example.test','Cache','active')`, tenant, user)
	must(`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'cache-epoch','Cache Epoch','active')`, tenant, product)
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'cache-epoch','Cache Epoch','draft')`, tenant, product, plan)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($2,$1,'cache-epoch','Cache Epoch','active')`, tenant, pool)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($2,$1,'cache-epoch','ready')`, tenant, server)

	t.Run("trigger catalog", func(t *testing.T) { checkEpochTriggers(t, ctx, admin) })
	t.Run("node catalog trigger compares every non-heartbeat column", func(t *testing.T) {
		checkNodeCatalogColumns(t, ctx, admin)
	})

	// 两条监听：缓存通道（四类）与节点通道（'d'、'c'），都以运行角色连库
	cacheWatch := NewWatch(CacheEpochChannel, "缓存", CacheKinds(), nil)
	nodeWatch := NewWatch(NodeEpochChannel, "节点", []string{"d", "c"}, nil)
	cacheCtx, stopCache := context.WithCancel(ctx)
	waitCache := cacheWatch.Start(cacheCtx, app.Pool, slog.Default())
	nodeCtx, stopNode := context.WithCancel(ctx)
	waitNode := nodeWatch.Start(nodeCtx, app.Pool, slog.Default())
	defer func() {
		stopCache()
		stopNode()
		waitCache()
		waitNode()
	}()
	for deadline := time.Now().Add(10 * time.Second); !cacheWatch.Healthy() || !nodeWatch.Healthy(); {
		if time.Now().After(deadline) {
			t.Fatal("epoch watches never became healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	epoch := func(seq string) int64 {
		t.Helper()
		var v int64
		if err := app.QueryRow(ctx, `SELECT `+EpochSQL(seq)).Scan(&v); err != nil {
			t.Fatalf("read %s as the app role: %v", seq, err)
		}
		return v
	}
	kindIndex := map[string]int{}
	for i, k := range CacheKinds() {
		kindIndex[k] = i
	}
	type signal struct {
		w    *Watch
		kind int
		seq  string
	}
	cacheSignal := func(kind string) signal { return signal{cacheWatch, kindIndex[kind], EpochSequence(kind)} }
	delivery := signal{nodeWatch, 0, NodeDeliveryEpoch}
	all := []signal{cacheSignal(KindNodeCatalog), cacheSignal(KindCatalog), cacheSignal(KindAppearance),
		cacheSignal(KindSiteSettings), delivery}

	// step 执行一次写（独立事务，提交后返回），断言 want 里的信号都在 2 秒内送达且序列前进，
	// 其余信号的序列没动。
	step := func(name string, want []signal, sql string, args ...any) {
		t.Helper()
		before := map[string]int64{}
		stamps := map[*Watch]Stamp{}
		for _, s := range all {
			before[s.seq] = epoch(s.seq)
			stamps[s.w] = s.w.Stamp()
			if !stamps[s.w].OK() {
				t.Fatalf("%s: watch on %s unhealthy before the write", name, s.w.Channel())
			}
		}
		start := time.Now()
		must(sql, args...)
		wanted := map[string]bool{}
		for _, s := range want {
			wanted[s.seq] = true
			for {
				now := s.w.Stamp()
				if now.OK() && now.Session() == stamps[s.w].Session() && now.Count(s.kind) > stamps[s.w].Count(s.kind) {
					break
				}
				if time.Since(start) > 2*time.Second {
					t.Fatalf("%s: no %s notification on %s within 2s of commit", name, s.seq, s.w.Channel())
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
		took := time.Since(start)
		for _, s := range all {
			after := epoch(s.seq)
			switch {
			case wanted[s.seq] && after <= before[s.seq]:
				t.Fatalf("%s: %s did not advance (%d -> %d); readers without the watch would miss it", name, s.seq, before[s.seq], after)
			case !wanted[s.seq] && after != before[s.seq]:
				t.Fatalf("%s: %s advanced (%d -> %d) on a write it does not cover", name, s.seq, before[s.seq], after)
			}
		}
		if len(want) > 0 {
			t.Logf("marker=cache_epoch_pg18_ok step=%q latency=%v", name, took.Round(time.Millisecond))
		}
	}
	nodeCatalog, catalog := cacheSignal(KindNodeCatalog), cacheSignal(KindCatalog)
	appearance, site := cacheSignal(KindAppearance), cacheSignal(KindSiteSettings)

	t.Run("node catalog", func(t *testing.T) {
		step("node added", []signal{nodeCatalog},
			`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,
				server_id,serving_status,protocol_schema_version,config_validated_at,last_heartbeat_at)
			  VALUES($2,$1,'cache-epoch-node',$3,'active','vless','cache-epoch.invalid',20443,$4,'active',1,now(),now())`,
			tenant, node, pool, server)
		step("routine heartbeat", nil, `UPDATE nodes SET last_heartbeat_at = now(), health_score = 90 WHERE id = $1`, node)
		step("connection port changed", []signal{nodeCatalog}, `UPDATE nodes SET server_port = 20444 WHERE id = $1`, node)
		step("heartbeat goes stale", nil, `UPDATE nodes SET last_heartbeat_at = now() - interval '11 minutes' WHERE id = $1`, node)
		step("heartbeat recovers", []signal{nodeCatalog}, `UPDATE nodes SET last_heartbeat_at = now() WHERE id = $1`, node)
		step("server leaves ready", []signal{nodeCatalog}, `UPDATE servers SET status = 'draft' WHERE id = $1`, server)
	})
	t.Run("catalog", func(t *testing.T) {
		step("stock counters only", nil, `UPDATE plans SET stock_reserved = stock_reserved + 1 WHERE id = $1`, plan)
		step("plan renamed", []signal{catalog}, `UPDATE plans SET name = 'Cache Epoch Renamed' WHERE id = $1`, plan)
	})
	t.Run("appearance", func(t *testing.T) {
		step("slot written", []signal{appearance},
			`INSERT INTO site_slots(tenant_id,slot_key,content) VALUES($1,'portal.footer','a')`, tenant)
	})
	t.Run("site settings and delivery", func(t *testing.T) {
		step("tenant touched", nil, `UPDATE tenants SET updated_at = now() WHERE id = $1`, tenant)
		step("site name", []signal{site}, `UPDATE tenants SET display_name = 'Cache Epoch Renamed' WHERE id = $1`, tenant)
		step("site timezone", []signal{site, delivery}, `UPDATE tenants SET timezone = 'Asia/Tokyo' WHERE id = $1`, tenant)
		step("switch added", []signal{site},
			`INSERT INTO feature_switches(tenant_id,code,enabled) VALUES($1,'cache.epoch.pg18',true)`, tenant)
		// 系统设置同时是节点下发的输入（设备判定，00101）
		step("setting added", []signal{site, delivery},
			`INSERT INTO system_settings(tenant_id,key,value) VALUES($1,'cache.epoch_pg18','1'::jsonb)`, tenant)
		step("user timezone", []signal{delivery}, `UPDATE users SET timezone = 'Asia/Tokyo' WHERE id = $1`, user)
		step("user display name", nil, `UPDATE users SET display_name = 'Cache Renamed' WHERE id = $1`, user)
	})

	t.Run("cache follows the watch, then falls back to the epoch", func(t *testing.T) {
		checkFreshnessFallback(t, ctx, app, cacheWatch, stopCache, waitCache, kindIndex[KindAppearance], tenant, must)
	})
}

// slotCount 是兜底用例里缓存的值：本租户插槽数与加载时的新鲜度。
type slotCount struct {
	n     int
	fresh Freshness
}

func checkFreshnessFallback(t *testing.T, ctx context.Context, app *db.Pool, w *Watch, stop func(), wait func(),
	kind int, tenant string, must func(string, ...any)) {
	seq := EpochSequence(KindAppearance)
	// TTL 一小时：下面每一次失效都只能来自纪元，不是 TTL
	c := New[string](Options[slotCount]{TTL: time.Hour, Max: 8})
	loads, epochReads := 0, 0
	get := func() int {
		t.Helper()
		want := Freshness{Stamp: w.Stamp()}
		if !want.Stamp.OK() {
			// 监听不健康：读一次纪元（真实读方把它并进本来就要跑的查询）
			epochReads++
			if err := app.QueryRow(ctx, `SELECT `+EpochSQL(seq)).Scan(&want.Epoch); err != nil {
				t.Fatal(err)
			}
		}
		v, err := c.Get(ctx, "slots", want.Flight(),
			func(e slotCount) bool { return e.fresh.Covers(want, kind) },
			func(e slotCount) bool { return e.fresh.Pinned(want, kind) },
			func(ctx context.Context) (slotCount, error) {
				loads++
				out := slotCount{fresh: Freshness{Stamp: w.Stamp()}} // 戳在查询之前取
				err := app.QueryRowScoped(ctx, db.Scope{TenantID: tenant},
					`SELECT count(*)::int, `+EpochSQL(seq)+` FROM site_slots WHERE tenant_id = $1`,
					[]any{tenant}, &out.n, &out.fresh.Epoch)
				return out, err
			})
		if err != nil {
			t.Fatal(err)
		}
		return v.n
	}
	within := func(name string, cond func(int) bool) {
		t.Helper()
		start := time.Now()
		for !cond(get()) {
			if time.Since(start) > 2*time.Second {
				t.Fatalf("%s: cache did not follow the commit within 2s", name)
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Logf("marker=cache_epoch_pg18_cache_ok step=%q latency=%v", name, time.Since(start).Round(time.Millisecond))
	}

	n0 := get()
	if get() != n0 || loads != 1 || epochReads != 0 {
		t.Fatalf("healthy watch: hit must not reload or read the epoch (loads=%d epochReads=%d)", loads, epochReads)
	}
	must(`INSERT INTO site_slots(tenant_id,slot_key,content) VALUES($1,'portal.home.banner','b')`, tenant)
	within("watched commit", func(n int) bool { return n == n0+1 })

	// 停掉监听：戳立即变零值，读方退回读纪元
	stop()
	wait()
	if w.Healthy() {
		t.Fatal("stopped watch still healthy")
	}
	loadsBefore := loads
	if get() != n0+1 || loads != loadsBefore {
		t.Fatalf("unwatched read with no commit reloaded: loads %d -> %d", loadsBefore, loads)
	}
	must(`INSERT INTO site_slots(tenant_id,slot_key,content) VALUES($1,'portal.home.aside','c')`, tenant)
	if got := get(); got != n0+2 {
		t.Fatalf("unwatched read after a commit served %d, want %d (fallback to the epoch failed)", got, n0+2)
	}
	t.Log("marker=cache_epoch_pg18_fallback_ok")
}

// checkEpochTriggers 核对每条纪元触发器都在：提交时推进（约束触发器、延迟到提交）、调对函数与种类。
func checkEpochTriggers(t *testing.T, ctx context.Context, admin *pgxpool.Pool) {
	type want struct{ table, trigger, fn, kind string }
	cases := []want{
		{"tenants", "zz_node_delivery_epoch_tenant_timezone", "bump_node_delivery_epoch", ""},
		{"users", "zz_node_delivery_epoch_users", "bump_node_delivery_epoch", ""},
		{"nodes", "zz_node_catalog_epoch_nodes_rows", "bump_cache_epoch", KindNodeCatalog},
		{"nodes", "zz_node_catalog_epoch_nodes_update", "bump_cache_epoch", KindNodeCatalog},
		{"servers", "zz_node_catalog_epoch_servers_update", "bump_cache_epoch", KindNodeCatalog},
		{"servers", "zz_node_catalog_epoch_servers_rows", "bump_cache_epoch", KindNodeCatalog},
		{"node_config_applications", "zz_node_catalog_epoch_config_applications", "bump_cache_epoch", KindNodeCatalog},
		{"plans", "zz_catalog_epoch_plans_rows", "bump_cache_epoch", KindCatalog},
		{"plans", "zz_catalog_epoch_plans_update", "bump_cache_epoch", KindCatalog},
		{"plan_versions", "zz_catalog_epoch_plan_versions", "bump_cache_epoch", KindCatalog},
		{"prices", "zz_catalog_epoch_prices", "bump_cache_epoch", KindCatalog},
		{"quota_definitions", "zz_catalog_epoch_quota_definitions", "bump_cache_epoch", KindCatalog},
		{"traffic_packs", "zz_catalog_epoch_traffic_packs", "bump_cache_epoch", KindCatalog},
		{"site_themes", "zz_appearance_epoch_site_themes", "bump_cache_epoch", KindAppearance},
		{"site_slots", "zz_appearance_epoch_site_slots", "bump_cache_epoch", KindAppearance},
		{"tenants", "zz_site_settings_epoch_tenants_rows", "bump_cache_epoch", KindSiteSettings},
		{"tenants", "zz_site_settings_epoch_tenants_update", "bump_cache_epoch", KindSiteSettings},
		{"system_settings", "zz_site_settings_epoch_system_settings", "bump_cache_epoch", KindSiteSettings},
		{"feature_switches", "zz_site_settings_epoch_feature_switches", "bump_cache_epoch", KindSiteSettings},
	}
	for _, c := range cases {
		var deferrable, deferred bool
		var def string
		err := admin.QueryRow(ctx, `
			SELECT t.tgdeferrable, t.tginitdeferred, pg_get_triggerdef(t.oid)
			  FROM pg_trigger t
			 WHERE t.tgrelid = ('public.' || $1)::regclass AND t.tgname = $2`, c.table, c.trigger).
			Scan(&deferrable, &deferred, &def)
		if err != nil {
			t.Errorf("%s on %s: %v", c.trigger, c.table, err)
			continue
		}
		call := "EXECUTE FUNCTION app." + c.fn + "()"
		if c.kind != "" {
			call = "EXECUTE FUNCTION app." + c.fn + "('" + c.kind + "')"
		}
		if !deferrable || !deferred || !strings.Contains(def, call) {
			t.Errorf("%s on %s: deferrable=%v deferred=%v, want %s in %s", c.trigger, c.table, deferrable, deferred, call, def)
		}
	}
	for _, k := range CacheKinds() {
		var ok bool
		if err := admin.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, EpochSequence(k)).Scan(&ok); err != nil || !ok {
			t.Errorf("sequence %s missing: %v", EpochSequence(k), err)
		}
	}
}

// checkNodeCatalogColumns：节点目录触发器比较 nodes 的每一列，只有心跳与遥测列除外（同 api/node 对
// 00122、00153 两个触发器的守卫）；已应用的发布物要比；last_heartbeat_at 只用于首次心跳与恢复心跳，
// 窗口 10 分钟（与 subscription.HeartbeatFreshWindow 相同，那边有对应的守卫）。
func checkNodeCatalogColumns(t *testing.T, ctx context.Context, admin *pgxpool.Pool) {
	var def string
	if err := admin.QueryRow(ctx, `SELECT pg_get_triggerdef(oid) FROM pg_trigger
		WHERE tgrelid = 'public.nodes'::regclass AND tgname = 'zz_node_catalog_epoch_nodes_update'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(def)
	if strings.Contains(lower, "to_jsonb") {
		t.Fatalf("trigger serializes whole rows: %s", def)
	}
	if !strings.Contains(lower, "'00:10:00'::interval") {
		t.Fatalf("heartbeat recovery window is not 10 minutes: %s", def)
	}
	telemetry := map[string]bool{"updated_at": true, "agent_version": true,
		"runtime_version": true, "config_signing_key_id": true, "applied_config_version": true,
		"applied_config_hash": true, "applied_effective_hash": true, "cpu_cores": true, "memory_mb": true,
		"disk_gb": true, "health_score": true}
	rows, err := admin.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'nodes' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		oldRef := regexp.MustCompile(`\bold\.` + regexp.QuoteMeta(col) + `\b`)
		newRef := regexp.MustCompile(`\bnew\.` + regexp.QuoteMeta(col) + `\b`)
		listed := oldRef.MatchString(lower) && newRef.MatchString(lower)
		switch {
		case telemetry[col] && (oldRef.MatchString(lower) || newRef.MatchString(lower)):
			t.Errorf("telemetry column %s is compared by the node catalog trigger", col)
		case !telemetry[col] && !listed:
			t.Errorf("nodes column %s is missing from the node catalog trigger; add it to the column list", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
