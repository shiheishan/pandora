package service

// 设备数限制 PG 测试的环境与夹具。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identityrepo "github.com/aegispanel/aegis/internal/module/identity/repo"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	settingsrepo "github.com/aegispanel/aegis/internal/module/settings/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subrepo "github.com/aegispanel/aegis/internal/module/subscription/repo"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

type devicesEnv struct {
	*deliveryEnv
}

func newDevicesEnv(t *testing.T) (*devicesEnv, context.Context) {
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
	st := settingssvc.New(settingsrepo.New(d), txm, nil, rdb, auditSvc)
	svc.UseSiteSettings(fakeSite{tz: "UTC"})
	svc.UseCatalog(cat)
	svc.UseDelivery(subs, ids, cat)
	svc.UseDevices(st, subs, ids, cat)
	de := &deliveryEnv{
		svc: svc, mr: mr, versionPlan: map[uuid.UUID]uuid.UUID{},
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	return &devicesEnv{deliveryEnv: de}, reqid.With(testkit.TxCtx(t, d), reqid.New())
}

func (e *devicesEnv) setPolicy(t *testing.T, ctx context.Context, mode string, grace, window int) {
	t.Helper()
	e.mustExec(t, ctx, `INSERT INTO settings (key, value) VALUES (?, ?::jsonb) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		"devices.mode", `"`+mode+`"`)
	e.mustExec(t, ctx, `INSERT INTO settings (key, value) VALUES (?, ?::jsonb) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		"devices.grace", strconv.Itoa(grace))
	e.mustExec(t, ctx, `INSERT INTO settings (key, value) VALUES (?, ?::jsonb) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		"devices.window_minutes", strconv.Itoa(window))
}

func (e *devicesEnv) alive(t *testing.T, ctx context.Context, node, sub uuid.UUID, device string, ago time.Duration) {
	t.Helper()
	secs := ago.Seconds()
	e.mustExec(t, ctx, `INSERT INTO node_alive_ips (node_id, subscription_id, ip_hash, last_seen_at)
		VALUES (?, ?, sha256(convert_to(?, 'UTF8')), now() - make_interval(secs => ?))`,
		node, sub, device, secs)
}

func (e *devicesEnv) clearAlive(t *testing.T, ctx context.Context) {
	t.Helper()
	e.mustExec(t, ctx, `DELETE FROM node_alive_ips`)
}

func (e *devicesEnv) epoch(t *testing.T) int64 {
	t.Helper()
	v, err := e.mr.Get(UsersEpochKey)
	if err != nil {
		return 0
	}
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			t.Fatalf("纪元 %q", v)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

type devicesSeed struct {
	p, srv, n1, n2, v, user uuid.UUID
	A, B, C                 fixtureSub
	userEmail               string
}

func seedDevices(t *testing.T, ctx context.Context, e *devicesEnv) *devicesSeed {
	t.Helper()
	g := e.dGroup(t, ctx)
	p := e.dPool(t, ctx, PoolActive, g)
	srv := e.dServer(t, ctx, "ready", ptrDur(time.Minute))
	n1 := e.dNode(t, ctx, &p, srv, "active", 30001, "dn1")
	n2 := e.dNode(t, ctx, &p, srv, "active", 30002, "dn2")
	v := e.dVersion(t, ctx, nil, intPtr(1), p)
	user := e.dUser(t, ctx, "active", &g)
	var email string
	if err := e.row(ctx, `SELECT email FROM users WHERE id = ?`, user).Scan(&email); err != nil {
		t.Fatal(err)
	}
	A := e.dSub(t, ctx, user, v, subOpts{})
	B := e.dSub(t, ctx, user, v, subOpts{})
	C := e.dSub(t, ctx, user, v, subOpts{DeviceLimit: intPtr(0)})
	return &devicesSeed{p: p, srv: srv, n1: n1, n2: n2, v: v, user: user, A: A, B: B, C: C, userEmail: email}
}
