package public

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// nodeNotices 在一条独占的管理连接上 LISTEN aegis_change，按节点 id 数收到的 nodes 变更通知。
type nodeNotices struct {
	t    *testing.T
	ctx  context.Context
	conn *pgxpool.Conn
}

func listenNodeNotices(t *testing.T, ctx context.Context, admin *pgxpool.Pool) *nodeNotices {
	t.Helper()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 带着 LISTEN 的连接不还回池里，测试结束直接关掉
	t.Cleanup(func() { _ = conn.Hijack().Close(context.Background()) })
	if _, err := conn.Exec(ctx, `LISTEN aegis_change`); err != nil {
		t.Fatal(err)
	}
	return &nodeNotices{t: t, ctx: ctx, conn: conn}
}

// drain 收下此刻已投递的全部通知，返回 nodes 表的按 id 计数。
func (l *nodeNotices) drain() map[string]int {
	l.t.Helper()
	// 通知在写事务提交后投递；先执行一条空语句把积压的收进来，再逐条非阻塞读
	if _, err := l.conn.Exec(l.ctx, `SELECT 1`); err != nil {
		l.t.Fatal(err)
	}
	got := map[string]int{}
	for {
		waitCtx, stop := context.WithTimeout(l.ctx, 300*time.Millisecond)
		note, err := l.conn.Conn().WaitForNotification(waitCtx)
		stop()
		if err != nil {
			if !pgconn.Timeout(err) || l.ctx.Err() != nil {
				l.t.Fatalf("wait for notifications: %v", err)
			}
			return got
		}
		var payload struct {
			Table string `json:"tbl"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal([]byte(note.Payload), &payload); err != nil {
			l.t.Fatalf("decode change notice %q: %v", note.Payload, err)
		}
		if payload.Table == "nodes" {
			got[payload.ID]++
		}
	}
}

// 迁移 00110：心跳类列的写不再发 nodes 变更通知；业务字段（名称、池、协议）照发；
// 两次心跳间隔达到 90 秒（离线 → 在线）时发一条。
func TestNodesChangeNotifyPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant = "7f710000-0000-4000-8000-000000000001"
		pool   = "7f710000-0000-4000-8000-0000000000a1"
		pool2  = "7f710000-0000-4000-8000-0000000000a2"
		server = "7f710000-0000-4000-8000-0000000000b1"
		node   = "7f710000-0000-4000-8000-0000000000c1"
	)
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\nSQL: %s", err, sql)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'nodes-notify-pg18','Nodes Notify','CNY')`, tenant)
	must(`INSERT INTO node_pools(id,tenant_id,code,name,status) VALUES($1,$3,'nn-a','NN A','active'),($2,$3,'nn-b','NN B','active')`, pool, pool2, tenant)
	must(`INSERT INTO servers(id,tenant_id,name,status) VALUES($1,$2,'nn-server','ready')`, server, tenant)
	listener := listenNodeNotices(t, ctx, admin)
	must(`INSERT INTO nodes(id,tenant_id,name,pool_id,status,node_type,server_host,server_port,server_id,serving_status)
		VALUES($1,$2,'nn-node',$3,'active','vless','nn.invalid',443,$4,'active')`, node, tenant, pool, server)

	expect := func(step string, want int) {
		t.Helper()
		got := listener.drain()
		if got[node] != want || len(got) > want {
			t.Fatalf("%s: node change notices=%v, want %d for the node", step, got, want)
		}
	}
	expect("insert", 1)

	nodes := nodefabric.NewService(app, nil)
	beat := func(cpu int) {
		t.Helper()
		if _, err := nodes.Heartbeat(ctx, tenant, node, nodefabric.HeartbeatInput{
			AgentVersion: "1.4.0", RuntimeVersion: "native-1", CPUCores: cpu, MemoryMB: 2048, DiskGB: 40,
			RuntimeStatus: "running",
		}); err != nil {
			t.Fatalf("heartbeat: %v", err)
		}
	}
	// 第一次心跳：从未上报到在线，界面要翻成「在线」
	beat(2)
	expect("first heartbeat", 1)
	// 稳态心跳：心跳时刻、资产、健康分、版本都在变，一条也不发
	beat(4)
	beat(4)
	expect("steady heartbeats", 0)
	must(`UPDATE nodes SET last_heartbeat_at=now(), health_score=40, agent_version='1.4.1',
		applied_config_version=7, applied_config_hash='\x01'::bytea WHERE id=$1`, node)
	must(`UPDATE nodes SET last_heartbeat_at=now(), health_score=90 WHERE id=$1`, node) // UniProxy 兼容端的写法
	expect("heartbeat-class columns", 0)

	// 掉线两分钟后恢复：间隔达到 90 秒的那次心跳发一条；把心跳往回拨本身不发
	must(`UPDATE nodes SET last_heartbeat_at=now() - interval '2 minutes' WHERE id=$1`, node)
	expect("rewind heartbeat", 0)
	beat(4)
	expect("offline → online", 1)

	// 业务字段照发
	must(`UPDATE nodes SET name='nn-node-renamed' WHERE id=$1`, node)
	expect("rename", 1)
	must(`UPDATE nodes SET pool_id=$2 WHERE id=$1`, node, pool2)
	expect("pool move", 1)
	must(`UPDATE nodes SET protocol_config='{"flow":"xtls-rprx-vision"}'::jsonb WHERE id=$1`, node)
	expect("protocol", 1)
	must(`UPDATE nodes SET node_type='trojan' WHERE id=$1`, node)
	expect("protocol type", 1)
	must(`UPDATE nodes SET serving_status='disabled' WHERE id=$1`, node)
	expect("serving status", 1)

	// 删除照发：nodes 级联到追加写的配置回执表，测试库里删不了节点（产品只走 destroyed），
	// 所以核对触发器定义本身——INSERT 与 DELETE 不带 WHEN，UPDATE 才带
	var insertDelete, update string
	if err := admin.QueryRow(ctx, `SELECT
			(SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid='public.nodes'::regclass AND tgname='zz_notify_nodes'),
			(SELECT pg_get_triggerdef(oid) FROM pg_trigger WHERE tgrelid='public.nodes'::regclass AND tgname='zz_notify_nodes_update')`,
	).Scan(&insertDelete, &update); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(insertDelete, "AFTER INSERT OR DELETE") || strings.Contains(insertDelete, "WHEN") ||
		!strings.Contains(update, "AFTER UPDATE") || !strings.Contains(update, "WHEN") {
		t.Fatalf("nodes notify triggers:\n%s\n%s", insertDelete, update)
	}
}
