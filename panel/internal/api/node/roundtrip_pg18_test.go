package node

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 这些检查是 TestSignedNodeHTTPPG18 的一部分（effective 域的 -run 只列顶层函数名）。

// roundTripCounter 是夹在应用连接池与 PostgreSQL 之间的 TCP 代理，数客户端发出的
// 同步点：每个 Sync（扩展协议）或 Query（简单协议）消息都要等服务端回 ReadyForQuery，
// 正好是一次网络往返。与实现无关：InTx、批次、语句准备都按线上的真实消息计。
type roundTripCounter struct {
	ln    net.Listener
	syncs atomic.Int64
	wg    sync.WaitGroup
}

func startRoundTripCounter(t *testing.T, upstream string) *roundTripCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &roundTripCounter{ln: ln}
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", upstream)
			if err != nil {
				_ = client.Close()
				continue
			}
			c.wg.Add(2)
			go func() { defer c.wg.Done(); _, _ = io.Copy(client, server); _ = client.Close() }()
			go func() { defer c.wg.Done(); c.pump(client, server); _ = server.Close() }()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return c
}

// pump 原样转发客户端字节，顺带按 PostgreSQL 前端协议切消息、数 Sync 与 Query。
// 启动阶段的消息没有类型字节（SSLRequest / GSSENCRequest 之后还会再来一条启动消息）。
func (c *roundTripCounter) pump(client, server net.Conn) {
	header := make([]byte, 5)
	untyped := true
	for {
		if untyped {
			if _, err := io.ReadFull(client, header[:4]); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint32(header[:4]))
			body := make([]byte, n-4)
			if _, err := io.ReadFull(client, body); err != nil {
				return
			}
			if _, err := server.Write(append(append([]byte{}, header[:4]...), body...)); err != nil {
				return
			}
			code := uint32(0)
			if len(body) >= 4 {
				code = binary.BigEndian.Uint32(body[:4])
			}
			untyped = code == 80877103 || code == 80877104 // SSL / GSSENC 协商后仍是无类型启动消息
			continue
		}
		if _, err := io.ReadFull(client, header); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(header[1:]))
		body := make([]byte, n-4)
		if _, err := io.ReadFull(client, body); err != nil {
			return
		}
		if header[0] == 'S' || header[0] == 'Q' {
			c.syncs.Add(1)
		}
		if _, err := server.Write(append(append([]byte{}, header...), body...)); err != nil {
			return
		}
	}
}

// memoryNonceStore 代替 Valkey：与 SET NX PX 同语义。
type memoryNonceStore struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (m *memoryNonceStore) ClaimNonce(_ context.Context, key string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.keys[key] {
		return false, nil
	}
	m.keys[key] = true
	return true, nil
}

// checkNodeHotPathPG18 量节点热路径每个端点的库往返数（完成标准 2），并核对心跳是 HOT
// 更新（完成标准 3）、遥测批次异步提交且不把设置带回连接池、心跳索引删掉之后没有查询吃亏。
func checkNodeHotPathPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, appDSN string,
	signer *platformcrypto.Signer, privateKey ed25519.PrivateKey, tenantID, nodeID, runtimeToken string,
	current nodefabric.SignedConfig) {
	t.Helper()
	dsn, err := url.Parse(appDSN)
	if err != nil {
		t.Fatal(err)
	}
	counter := startRoundTripCounter(t, dsn.Host)
	dsn.Host = counter.ln.Addr().String()
	app, err := db.OpenWithOptions(ctx, dsn.String(), db.Options{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	t.Run("round trips per endpoint", func(t *testing.T) {
		for _, mode := range []struct {
			name   string
			valkey bool
		}{{"valkey", true}, {"pg-fallback", false}} {
			svc := nodefabric.NewService(app, signer)
			svc.SetReleaseBinding(nodefabric.ReleaseBinding{})
			svc.EnableNodeCaches()
			if mode.valkey {
				svc.SetNonceStore(&memoryNonceStore{keys: map[string]bool{}}, nil)
			}
			hub := nodefabric.NewStreamHub()
			svc.AttachStream(hub)
			server := httptest.NewServer(NewRouter(Deps{Pool: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Node: svc, NodeStream: hub}))

			uni := func(path string) string {
				return fmt.Sprintf("%s/api/v1/server/UniProxy/%s?node_id=%s&node_type=shadowsocks", server.URL, path, nodeID)
			}
			var etag string
			endpoints := []struct {
				name   string
				perMin float64 // r3 实测每节点每分钟的请求数
				do     func() int
			}{
				{"effective-config 未变", 4, func() int {
					req := newSignedRequest(t, privateKey, nodeID, http.MethodGet, server.URL+"/v1/nodes/effective-config",
						nil, freshNonce(t), time.Now().UTC().Format(time.RFC3339))
					req.Header.Set(nodefabric.AppliedEffectiveReleaseHeader,
						current.ReleaseID+"/"+strconv.FormatUint(current.Generation, 10))
					return send(t, req)
				}},
				{"UniProxy user 未变", 4, func() int {
					req, _ := http.NewRequest(http.MethodGet, uni("user"), nil)
					req.Header.Set("Authorization", "Bearer "+runtimeToken)
					if etag != "" {
						req.Header.Set("If-None-Match", etag)
					}
					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					etag = resp.Header.Get("ETag")
					return resp.StatusCode
				}},
				{"heartbeat", 2, func() int {
					req := newSignedRequest(t, privateKey, nodeID, http.MethodPost, server.URL+"/v1/nodes/heartbeat",
						[]byte(`{"agent_version":"rt-test","runtime_version":"native-rt","config_signing_key_id":"`+signer.KeyID()+
							`","metrics":{"cpu_bp":1200,"mem_used_mb":512,"mem_total_mb":2048}}`),
						freshNonce(t), time.Now().UTC().Format(time.RFC3339))
					req.Header.Set("Content-Type", "application/json")
					return send(t, req)
				}},
				{"push", 1, func() int {
					req, _ := http.NewRequest(http.MethodPost, uni("push"), strings.NewReader(
						`{"`+strconv.Itoa(int(time.Now().UnixNano()%1_000_000)+900_000_000)+`":[1024,2048]}`))
					req.Header.Set("Authorization", "Bearer "+runtimeToken)
					return send(t, req)
				}},
				{"alive", 1, func() int {
					req, _ := http.NewRequest(http.MethodPost, uni("alive"), strings.NewReader(`{"900000001":["198.18.0.1"]}`))
					req.Header.Set("Authorization", "Bearer "+runtimeToken)
					return send(t, req)
				}},
			}
			var weighted float64
			for _, ep := range endpoints {
				// 前两次把各连接上的语句准备好（稳态走语句缓存），再量三次取平均
				for i := 0; i < 2; i++ {
					if code := ep.do(); code >= 300 && code != http.StatusNotModified {
						t.Fatalf("%s %s warm-up = %d", mode.name, ep.name, code)
					}
				}
				const runs = 3
				before := counter.syncs.Load()
				for i := 0; i < runs; i++ {
					if code := ep.do(); code >= 300 && code != http.StatusNotModified {
						t.Fatalf("%s %s = %d", mode.name, ep.name, code)
					}
				}
				per := float64(counter.syncs.Load()-before) / runs
				weighted += per * ep.perMin
				t.Logf("[%s] %s：每请求 %.1f 次库往返（每节点每分钟 %.0f 次请求）", mode.name, ep.name, per, ep.perMin)
			}
			t.Logf("[%s] 节点链路每节点每分钟库往返 ≈ %.1f（不含 0.1 次/分的换钥检查；push 不含有效用户的扣量语句）",
				mode.name, weighted)
			if mode.valkey && weighted > 25 {
				t.Errorf("node hot path = %.1f round trips per node-minute, want ≤ 25", weighted)
			}
			server.Close()
		}
	})

	t.Run("heartbeat is a HOT update", func(t *testing.T) {
		svc := nodefabric.NewService(app, signer)
		read := func() (upd, hot int64) {
			t.Helper()
			time.Sleep(1500 * time.Millisecond) // 后端统计每秒至多刷新一次
			if _, err := admin.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
				t.Fatal(err)
			}
			if err := admin.QueryRow(ctx, `SELECT n_tup_upd, n_tup_hot_upd FROM pg_stat_user_tables
				WHERE relid = 'public.nodes'::regclass`).Scan(&upd, &hot); err != nil {
				t.Fatal(err)
			}
			return upd, hot
		}
		in := nodefabric.HeartbeatInput{AgentVersion: "hot-test", RuntimeVersion: "native-hot", CPUCores: 2,
			MemoryMB: 2048, DiskGB: 40, RuntimeStatus: "running", ConfigSigningKeyID: signer.KeyID()}
		if _, err := svc.Heartbeat(ctx, tenantID, nodeID, in); err != nil {
			t.Fatal(err)
		}
		upd0, hot0 := read()
		const beats = 40
		for i := 0; i < beats; i++ {
			in.Metrics = &nodefabric.Metrics{CPUBasisPoints: 100 + i}
			if _, err := svc.Heartbeat(ctx, tenantID, nodeID, in); err != nil {
				t.Fatal(err)
			}
		}
		upd1, hot1 := read()
		updates, hotUpdates := upd1-upd0, hot1-hot0
		t.Logf("%d 次心跳：nodes 更新 %d 行，其中 HOT %d 行（%.0f%%）", beats, updates, hotUpdates,
			100*float64(hotUpdates)/float64(max(updates, 1)))
		if updates < beats || float64(hotUpdates) < 0.9*float64(updates) {
			t.Fatalf("heartbeat HOT ratio %d/%d below 90%%", hotUpdates, updates)
		}
		var fillfactor string
		if err := admin.QueryRow(ctx, `SELECT coalesce(array_to_string(reloptions, ','), '')
			FROM pg_class WHERE oid = 'public.nodes'::regclass`).Scan(&fillfactor); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fillfactor, "fillfactor=85") {
			t.Fatalf("nodes reloptions = %q, want fillfactor=85", fillfactor)
		}
	})

	t.Run("telemetry batches commit asynchronously without leaking the setting", func(t *testing.T) {
		var during, after string
		b := &pgx.Batch{}
		b.Queue(`SELECT current_setting('synchronous_commit')`).QueryRow(func(row pgx.Row) error { return row.Scan(&during) })
		if err := app.BatchScoped(ctx, db.Scope{TenantID: tenantID}, db.BatchOptions{AsyncCommit: true}, b); err != nil {
			t.Fatal(err)
		}
		// 池里只有一条连接：下一条语句一定落在同一条连接上
		if err := app.QueryRowScoped(ctx, db.Scope{TenantID: tenantID},
			`SELECT current_setting('synchronous_commit')`, nil, &after); err != nil {
			t.Fatal(err)
		}
		if during != "off" || after != "on" {
			t.Fatalf("synchronous_commit during=%q after=%q, want off then on", during, after)
		}
	})

	t.Run("no query relied on the dropped heartbeat index", func(t *testing.T) {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// 在回滚的事务里原样重建 00005 的索引，关掉顺序扫描逼规划器尽量走索引：
		// 谓词推不出索引条件的查询仍然用不上它。
		for _, sql := range []string{
			`CREATE INDEX idx_nodes_heartbeat_probe ON nodes (last_heartbeat_at) WHERE status IN ('standby', 'canary', 'active')`,
			`SET LOCAL enable_seqscan = off`,
		} {
			if _, err := tx.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		t1 := "'" + tenantID + "'"
		for name, sql := range map[string]string{
			"dashboard nodes_offline": `SELECT n.id FROM nodes n WHERE n.tenant_id = ` + t1 + ` AND n.status <> 'destroyed'
				AND n.serving_status <> 'retired' AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < now() - interval '90 seconds')
				ORDER BY n.last_heartbeat_at ASC NULLS LAST, n.id LIMIT 3`,
			"online counts": `SELECT count(*), count(*) FILTER (WHERE last_heartbeat_at >= now() - interval '90 seconds')
				FROM nodes WHERE tenant_id = ` + t1 + ` AND status <> 'destroyed' AND serving_status <> 'retired'`,
			"routing online": `SELECT count(*) FROM nodes WHERE tenant_id = ` + t1 + ` AND status <> 'destroyed'
				AND serving_status = 'active' AND last_heartbeat_at >= now() - interval '90 seconds'`,
			"liveness patrol": `SELECT n.id FROM nodes n WHERE n.tenant_id = ` + t1 + ` AND n.status <> 'destroyed'
				AND n.serving_status <> 'retired' AND n.last_heartbeat_at IS NOT NULL
				AND n.last_heartbeat_at > now() - interval '5 minutes' AND n.last_heartbeat_at <= now() - interval '90 seconds'`,
			"node list stale filter": `SELECT n.id FROM nodes n WHERE n.tenant_id = ` + t1 + ` AND n.serving_status <> 'retired'
				AND (n.last_heartbeat_at IS NULL OR n.last_heartbeat_at < now() - interval '90 seconds')`,
		} {
			var plan strings.Builder
			rows, err := tx.Query(ctx, `EXPLAIN `+sql)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(line + "\n")
			}
			rows.Close()
			if strings.Contains(plan.String(), "idx_nodes_heartbeat_probe") {
				t.Fatalf("%s would use the dropped heartbeat index:\n%s", name, plan.String())
			}
		}
	})

	t.Run("notify trigger compares every non-heartbeat column", func(t *testing.T) {
		var def string
		if err := admin.QueryRow(ctx, `SELECT pg_get_triggerdef(oid) FROM pg_trigger
			WHERE tgrelid = 'public.nodes'::regclass AND tgname = 'zz_notify_nodes_update'`).Scan(&def); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(def, "to_jsonb") {
			t.Fatalf("trigger still serializes whole rows: %s", def)
		}
		heartbeatColumns := map[string]bool{"last_heartbeat_at": true, "updated_at": true, "agent_version": true,
			"runtime_version": true, "config_signing_key_id": true, "applied_config_version": true,
			"applied_config_hash": true, "applied_effective_release_id": true, "applied_effective_generation": true,
			"applied_effective_hash": true, "cpu_cores": true, "memory_mb": true, "disk_gb": true, "health_score": true}
		rows, err := admin.Query(ctx, `SELECT column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'nodes' ORDER BY ordinal_position`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		lower := strings.ToLower(def)
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				t.Fatal(err)
			}
			listed := strings.Contains(lower, "old."+col+",") || strings.Contains(lower, "old."+col+")")
			if heartbeatColumns[col] && listed && col != "last_heartbeat_at" {
				t.Errorf("heartbeat column %s is compared by the notify trigger", col)
			}
			if !heartbeatColumns[col] && !listed {
				t.Errorf("nodes column %s is missing from zz_notify_nodes_update; add it to the column list", col)
			}
		}
	})
}

func send(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}
