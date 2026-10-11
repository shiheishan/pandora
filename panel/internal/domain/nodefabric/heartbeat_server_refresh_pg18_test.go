package nodefabric

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// checkHeartbeatServerRefreshPG18 挂在 TestServerBindingPG18 下（server_binding 域，同库同租户）：
// 心跳批量写里的服务器 UPDATE 做了空集短路（w12period），要证明
//   - 有同 id 服务器行的节点照样刷新 servers.last_heartbeat_at（含 45 秒刷新间隔的口径）；
//   - 没有的节点核对一次之后不再带进 UPDATE，整批都没有就不发这条语句；
//   - 服务器行后来才建、而节点换了身份（引导与接入的做法）时，下一拍就刷新；
//   - 判断过了 hbServerKnowledgeTTL 重新核对。
func checkHeartbeatServerRefreshPG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, app *db.Pool, tenant string) {
	t.Helper()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	withSrv, noSrv := uuid.NewString(), uuid.NewString()
	pkA, pkB, pkB2 := []byte("hbsrv-key-a"), []byte("hbsrv-key-b"), []byte("hbsrv-key-b-rotated")
	must(`INSERT INTO servers (id, tenant_id, name, status) VALUES ($1, $2, $3, 'ready')`, withSrv, tenant, "hbsrv-"+withSrv)
	for _, n := range []struct {
		id, server string
		pk         []byte
	}{{withSrv, withSrv, pkA}, {noSrv, "", pkB}} {
		var server any
		if n.server != "" {
			server = n.server
		}
		must(`INSERT INTO nodes (id, tenant_id, name, status, serving_status, server_id) VALUES ($1, $2, $3, 'active', 'active', $4)`,
			n.id, tenant, "hbsrv-node-"+n.id, server)
		must(`INSERT INTO node_identities (tenant_id, node_id, serial, public_key, spiffe_id, fingerprint, expires_at)
			VALUES ($1, $2, 1, $3, $4, $5, now() + interval '90 days')`,
			tenant, n.id, n.pk, "spiffe://aegis/test/"+n.id, []byte("hbsrv-fp-"+n.id))
	}
	svc := NewService(app, nil)
	c := newHeartbeatCoalescer()
	svc.hb = c
	keyYes, keyNo := hbKey(tenant, withSrv), hbKey(tenant, noSrv)
	in := HeartbeatInput{AgentVersion: "hbsrv"}
	base := time.Now().Add(-5 * time.Minute)
	// 模拟两个节点都刚立即写过一拍（base），之后的拍进缓冲
	c.written(keyYes, in, base, pkA)
	c.written(keyNo, in, base, pkB)
	beat := func(key string, pk []byte, at time.Time) {
		t.Helper()
		if !c.offer(key, in, at, pk) {
			t.Fatalf("beat at +%s was not buffered", at.Sub(base))
		}
	}
	serverBeat := func(id string) *time.Time {
		t.Helper()
		var at *time.Time
		if err := admin.QueryRow(ctx, `SELECT last_heartbeat_at FROM servers WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	counters := func() (statements, probes int) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.serverStatements, c.serverProbes
	}
	expectWork := func(step string, wantStatements, wantProbes int) {
		t.Helper()
		if s, p := counters(); s != wantStatements || p != wantProbes {
			t.Fatalf("%s: server statements=%d probes=%d, want %d/%d", step, s, p, wantStatements, wantProbes)
		}
	}

	// 1. 第一次批量写：两个节点都没核对过，UPDATE 带上两个、探测一次；有服务器行的刷新了
	beat(keyYes, pkA, base.Add(20*time.Second))
	beat(keyNo, pkB, base.Add(20*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("first flush", 1, 1)
	first := serverBeat(withSrv)
	if first == nil || !first.Equal(base.Add(20*time.Second).Truncate(time.Microsecond)) {
		t.Fatalf("server row heartbeat after the first flush = %v, want the buffered beat", first)
	}
	if k := c.nodes[keyYes].server; !k.known || !k.has {
		t.Fatalf("node with a server row was not recorded as having one: %+v", k)
	}
	if k := c.nodes[keyNo].server; !k.known || k.has {
		t.Fatalf("node without a server row was not recorded as having none: %+v", k)
	}

	// 2. 核对过之后：UPDATE 只带有服务器行的节点，不再探测；45 秒刷新间隔内不重写
	beat(keyYes, pkA, base.Add(40*time.Second))
	beat(keyNo, pkB, base.Add(40*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("second flush", 2, 1)
	if got := serverBeat(withSrv); got == nil || !got.Equal(*first) {
		t.Fatalf("server row rewritten inside the refresh interval: %v -> %v", first, got)
	}

	// 3. 整批都是没有服务器行的节点：不发服务器那条语句
	beat(keyNo, pkB, base.Add(60*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("flush with only server-less nodes", 2, 1)

	// 4. 过了刷新间隔，有服务器行的节点照样刷新
	beat(keyYes, pkA, base.Add(80*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("refresh flush", 3, 1)
	if got := serverBeat(withSrv); got == nil || !got.Equal(base.Add(80*time.Second).Truncate(time.Microsecond)) {
		t.Fatalf("server row not refreshed after the interval: %v", got)
	}

	// 5. 服务器行后建、节点换了身份（引导与接入同时做这两件事）：下一拍立即写清掉判断，批量写重新核对并刷新
	must(`INSERT INTO servers (id, tenant_id, name, status) VALUES ($1, $2, $3, 'ready')`, noSrv, tenant, "hbsrv-late-"+noSrv)
	must(`UPDATE node_identities SET public_key = $3 WHERE tenant_id = $1 AND node_id = $2`, tenant, noSrv, pkB2)
	if c.offer(keyNo, in, base.Add(90*time.Second), pkB2) {
		t.Fatal("beat with a rotated identity was buffered")
	}
	c.written(keyNo, in, base.Add(90*time.Second), pkB2) // 立即写成功的记录（Service.heartbeat 会做）
	if k := c.nodes[keyNo].server; k.known {
		t.Fatalf("rotating the identity did not clear the server knowledge: %+v", k)
	}
	beat(keyNo, pkB2, base.Add(100*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("flush after identity rotation", 4, 2)
	if got := serverBeat(noSrv); got == nil || !got.Equal(base.Add(100*time.Second).Truncate(time.Microsecond)) {
		t.Fatalf("late server row not refreshed after the identity rotated: %v", got)
	}
	if k := c.nodes[keyNo].server; !k.known || !k.has {
		t.Fatalf("late server row not learned: %+v", k)
	}

	// 6. 判断过了 TTL：重新核对
	c.mu.Lock()
	c.nodes[keyYes].server.at = time.Now().Add(-hbServerKnowledgeTTL - time.Minute)
	c.mu.Unlock()
	beat(keyYes, pkA, base.Add(120*time.Second))
	svc.FlushHeartbeats(ctx)
	expectWork("flush after the knowledge expired", 5, 3)

	// 7. 取走之后判断被清空的节点，批量写学到的结果不记（serverGen 对不上）
	beat(keyYes, pkA, base.Add(140*time.Second))
	taken := c.take(time.Now())
	if len(taken) != 1 {
		t.Fatalf("taken %d rows, want 1", len(taken))
	}
	c.written(keyYes, HeartbeatInput{AgentVersion: "hbsrv-v2"}, base.Add(150*time.Second), pkA) // 材料变了：清空判断
	c.learnServers(taken, map[string]bool{}, time.Now())
	if k := c.nodes[keyYes].server; k.known {
		t.Fatalf("a stale probe result was recorded after the knowledge was cleared: %+v", k)
	}
}
