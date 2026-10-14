package service

// 流量、在线、指标入账 PG 测试的环境与夹具。扣量用假实现（扣量本身在 subscription 模块另有 PG 测试）；
// 订阅、服务器、节点用最小 SQL 行。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakeCharger 是内存里的 TrafficCharger：按 subs 回 uid → 订阅，记下每次扣量的入参。
type fakeCharger struct {
	mu    sync.Mutex
	subs  map[int64]uuid.UUID
	calls [][]subsvc.TrafficCharge
}

func (f *fakeCharger) lookup(uids []int64) map[int64]uuid.UUID {
	out := map[int64]uuid.UUID{}
	for _, uid := range uids {
		if id, ok := f.subs[uid]; ok {
			out[uid] = id
		}
	}
	return out
}

func (f *fakeCharger) ChargeTraffic(_ context.Context, charges []subsvc.TrafficCharge) (map[int64]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]subsvc.TrafficCharge(nil), charges...))
	uids := make([]int64, 0, len(charges))
	for _, c := range charges {
		uids = append(uids, c.UID)
	}
	return f.lookup(uids), nil
}

func (f *fakeCharger) SubscriptionIDsByNodeUIDs(_ context.Context, uids []int64) (map[int64]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookup(uids), nil
}

func (f *fakeCharger) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// trafficEnv 是跑在回滚事务里的环境（提交后钩子不执行）。服务的时钟钉在当前整点后 30 分钟，hour 是那个整点，
// 免得测试跨整点时小时汇总落到两行。
type trafficEnv struct {
	svc     *Service
	charger *fakeCharger
	hour    time.Time
	exec    func(ctx context.Context, q string, args ...any) error
	row     func(ctx context.Context, q string, args ...any) *sql.Row
}

func newTrafficEnv(t *testing.T) (*trafficEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	svc := New(repo.New(d), tx.NewManager(d), nil, nil)
	ch := &fakeCharger{subs: map[int64]uuid.UUID{}}
	svc.UseTrafficCharger(ch)
	svc.UseKeys(testDeviceKeys{})
	fixed := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	svc.now = func() time.Time { return fixed }
	e := &trafficEnv{
		svc: svc, charger: ch, hour: fixed.Truncate(time.Hour),
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	return e, reqid.With(testkit.TxCtx(t, d), reqid.New())
}

func (e *trafficEnv) mustExec(t *testing.T, ctx context.Context, q string, args ...any) {
	t.Helper()
	if err := e.exec(ctx, q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func (e *trafficEnv) count(t *testing.T, ctx context.Context, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	return n
}

// seedTrafficServer 插一台服务器。
func seedTrafficServer(t *testing.T, ctx context.Context, e *trafficEnv) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO servers (name) VALUES (?) RETURNING id`, poolRandName(t, "tsrv")).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedTrafficNode 在服务器上插一个草稿节点（serverID 为 nil 时不挂服务器），倍率如 "1.50"。
func seedTrafficNode(t *testing.T, ctx context.Context, e *trafficEnv, serverID *uuid.UUID, rate string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO nodes (name, server_id, traffic_rate) VALUES (?, ?, ?::numeric) RETURNING id`,
		poolRandName(t, "tnode"), serverID, rate).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedTrafficSub 插最小的一份订阅，并登记进假扣量的 uid → 订阅。
func seedTrafficSub(t *testing.T, ctx context.Context, e *trafficEnv) (uuid.UUID, int64) {
	t.Helper()
	var userID, planID, versionID, subID uuid.UUID
	var uid int64
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, poolRandName(t, "tu")+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO plans (code, name) VALUES (?, ?) RETURNING id`, poolRandName(t, "tp"), "流量测试套餐").Scan(&planID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO plan_versions (plan_id, version, max_devices) VALUES (?, 1, 3) RETURNING id`, planID).Scan(&versionID); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `INSERT INTO subscriptions (user_id, plan_id, plan_version_id, current_period_start, current_period_end,
		snapshot_currency, snapshot_amount, snapshot_interval, snapshot_interval_count)
		VALUES (?, ?, ?, now(), now() + interval '30 days', 'CNY', 10, 'month', 1) RETURNING id, node_uid`,
		userID, planID, versionID).Scan(&subID, &uid); err != nil {
		t.Fatal(err)
	}
	e.charger.subs[uid] = subID
	return subID, uid
}

// testDeviceKeys 是测试用的设备键 HMAC（固定密钥）。
type testDeviceKeys struct{}

func (testDeviceKeys) HMAC(info string, data []byte) []byte {
	m := hmac.New(sha256.New, []byte("test-"+info))
	m.Write(data)
	return m.Sum(nil)
}
