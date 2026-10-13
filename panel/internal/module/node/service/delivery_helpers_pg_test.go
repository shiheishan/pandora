package service

// 名单与线路 PG 测试的环境与夹具：subscription、identity、catalog 都用真 service（读取各自的表），
// 夹具全部用 SQL 插在回滚事务里。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

type deliveryEnv struct {
	svc  *Service
	mr   *miniredis.Miniredis
	exec func(ctx context.Context, q string, args ...any) error
	row  func(ctx context.Context, q string, args ...any) *sql.Row
	// versionPlan 是版本 → 套餐（插订阅要两个都给）
	versionPlan map[uuid.UUID]uuid.UUID
}

func newDeliveryEnv(t *testing.T) (*deliveryEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	mr, rdb := testkit.Redis(t)
	txm := tx.NewManager(d)
	svc := New(repo.New(d), txm, auditSvc, rdb)
	cat := catalogsvc.New(catalogrepo.New(d), txm, auditSvc, nil)
	subs := subsvc.New(subrepo.New(d), txm, auditSvc, nil, nil, nil, nil)
	ids := identitysvc.New(identityrepo.New(d), txm, nil, auditSvc, keys, rdb, ratelimit.New(rdb), &config.Config{})
	svc.UseCatalog(cat)
	svc.UseDelivery(subs, ids, cat)
	e := &deliveryEnv{
		svc: svc, mr: mr, versionPlan: map[uuid.UUID]uuid.UUID{},
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	return e, reqid.With(testkit.TxCtx(t, d), reqid.New())
}

func (e *deliveryEnv) mustExec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *deliveryEnv) insertID(t *testing.T, ctx context.Context, q string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, q, args...).Scan(&id); err != nil {
		t.Fatalf("insert %q: %v", q, err)
	}
	return id
}

func (e *deliveryEnv) dGroup(t *testing.T, ctx context.Context) uuid.UUID {
	return e.insertID(t, ctx, `INSERT INTO user_groups (code, name) VALUES (?, 'G') RETURNING id`, poolRandName(t, "g"))
}

func (e *deliveryEnv) dPool(t *testing.T, ctx context.Context, status string, groups ...uuid.UUID) uuid.UUID {
	code := poolRandName(t, "p")
	id := e.insertID(t, ctx, `INSERT INTO node_pools (code, name, status) VALUES (?, ?, ?) RETURNING id`, code, "池 "+code, status)
	for _, g := range groups {
		e.mustExec(t, ctx, `INSERT INTO node_pool_user_groups (pool_id, user_group_id) VALUES (?, ?)`, id, g)
	}
	return id
}

// dServer 插一台服务器；heartbeatAgo 为 nil 时没有心跳。
func (e *deliveryEnv) dServer(t *testing.T, ctx context.Context, status string, heartbeatAgo *time.Duration) uuid.UUID {
	var beat *time.Time
	if heartbeatAgo != nil {
		at := time.Now().Add(-*heartbeatAgo)
		beat = &at
	}
	return e.insertID(t, ctx, `INSERT INTO servers (name, status, last_heartbeat_at, retired_at)
		VALUES (?, ?, ?, CASE WHEN ? = 'retired' THEN now() END) RETURNING id`,
		poolRandName(t, "srv"), status, beat, status)
}

// dNode 插一个写全协议列的节点（active 或 disabled），display 是线路里的名字。
func (e *deliveryEnv) dNode(t *testing.T, ctx context.Context, pool *uuid.UUID, server uuid.UUID, serving string, port int, display string) uuid.UUID {
	return e.insertID(t, ctx, `INSERT INTO nodes (name, display_name, pool_id, server_id, serving_status, node_type, server_host,
		server_port, listen_l4, protocol_schema_version, protocol_config)
		VALUES (?, ?, ?, ?, ?, 'vless', 'node.example.test', ?, 'tcp', 1, '{"flow":"xtls-rprx-vision"}'::jsonb) RETURNING id`,
		poolRandName(t, "n"), display, pool, server, serving, port)
}

// dVersion 建一个套餐与一个草稿版本并绑池（草稿才能改绑池；VersionsByIDs 与 VersionPools 不看状态）。
func (e *deliveryEnv) dVersion(t *testing.T, ctx context.Context, throttle, maxDevices *int, pools ...uuid.UUID) uuid.UUID {
	planID := e.insertID(t, ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`, poolRandName(t, "pl"), "测试套餐")
	vid := e.insertID(t, ctx, `INSERT INTO plan_versions (plan_id, version, throttle_kbps, max_devices) VALUES (?, 1, ?, ?) RETURNING id`,
		planID, throttle, maxDevices)
	for _, p := range pools {
		e.mustExec(t, ctx, `INSERT INTO plan_node_pools (plan_version_id, pool_id) VALUES (?, ?)`, vid, p)
	}
	e.versionPlan[vid] = planID
	return vid
}

func (e *deliveryEnv) dUser(t *testing.T, ctx context.Context, status string, group *uuid.UUID) uuid.UUID {
	return e.insertID(t, ctx, `INSERT INTO users (email, status, user_group_id) VALUES (?, ?, ?) RETURNING id`,
		poolRandName(t, "u")+"@example.test", status, group)
}

// subOpts 是夹具订阅的变化项；零值是生效中、没有配额行、没有流量包。
type subOpts struct {
	Expired       bool
	PastEnd       bool
	DeviceLimit   *int
	QuotaLimit    *int64
	QuotaConsumed int64
	PackGranted   int64
	PackConsumed  int64
}

type fixtureSub struct {
	ID    uuid.UUID
	UID   int64
	Proxy uuid.UUID
}

func (e *deliveryEnv) dSub(t *testing.T, ctx context.Context, user, version uuid.UUID, o subOpts) fixtureSub {
	t.Helper()
	status := "active"
	if o.Expired {
		status = "expired"
	}
	start, end := "now() - interval '1 day'", "now() + interval '30 days'"
	if o.PastEnd {
		start, end = "now() - interval '2 days'", "now() - interval '1 hour'"
	}
	var s fixtureSub
	err := e.row(ctx, `INSERT INTO subscriptions (user_id, plan_id, plan_version_id, status, current_period_start,
		current_period_end, snapshot_currency, snapshot_amount, snapshot_interval, snapshot_interval_count, device_limit)
		VALUES (?, ?, ?, ?, `+start+`, `+end+`, 'CNY', 10, 'month', 1, ?) RETURNING id, node_uid, proxy_uuid`,
		user, e.versionPlan[version], version, status, o.DeviceLimit).Scan(&s.ID, &s.UID, &s.Proxy)
	if err != nil {
		t.Fatalf("插订阅: %v", err)
	}
	if o.QuotaLimit != nil {
		e.mustExec(t, ctx, `INSERT INTO quota_balances (subscription_id, metric, period, period_start, period_end, limit_value, consumed)
			VALUES (?, 'traffic.bytes', 'cycle', now() - interval '1 day', now() + interval '30 days', ?, ?)`,
			s.ID, *o.QuotaLimit, o.QuotaConsumed)
	}
	if o.PackGranted > 0 {
		e.mustExec(t, ctx, `INSERT INTO traffic_pack_grants (user_id, subscription_id, source, source_id, granted_bytes, consumed_bytes)
			VALUES (?, ?, 'admin', ?, ?, ?)`, user, s.ID, uuid.New(), o.PackGranted, o.PackConsumed)
	}
	return s
}

func rosterOf(rosters map[uuid.UUID][]NodeRoster, server, node uuid.UUID) (NodeRoster, bool) {
	for _, r := range rosters[server] {
		if r.NodeID == node {
			return r, true
		}
	}
	return NodeRoster{}, false
}

func hasUID(r NodeRoster, uid int64) (RosterUser, bool) {
	for _, u := range r.Users {
		if u.UID == uid {
			return u, true
		}
	}
	return RosterUser{}, false
}

func lineNames(nodes []render.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Name
	}
	return out
}

func ptrDur(d time.Duration) *time.Duration { return &d }
