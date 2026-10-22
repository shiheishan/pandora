package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/report/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// pgEnv 是跑在回滚事务里的真库环境：本模块 repo、tx、audit 用真的，属主模块端口用 fakePorts（站点时区 Asia/Shanghai）。
type pgEnv struct {
	svc   *Service
	ports *fakePorts
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	exec  func(ctx context.Context, q string, args ...any) error
}

// newPGEnv 构造真库环境；没有 PANDORA_TEST_DATABASE_URL 时 testkit 跳过用例。返回的 ctx 在回滚事务里。
func newPGEnv(t *testing.T) (*pgEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x02
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	au, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	p := newFakePorts()
	deps := Deps{
		Settings: p, Users: p, Subscriptions: p, Nodes: p, Ledger: p, Orders: p, Payments: p,
		Commissions: p, Giftcards: p, Tickets: p, Notices: p, Activity: p, Redis: &fakeRedis{},
	}
	svc := New(repo.New(d), tx.NewManager(d), au, deps, Options{})

	ctx := testkit.TxCtx(t, d)
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.9", UserAgent: "report-test/1.0"})
	e := &pgEnv{
		svc:   svc,
		ports: p,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}

// asAdmin 插一个用户当登记人，返回带后台主体的 ctx 与用户 id。
func (e *pgEnv) asAdmin(t *testing.T, ctx context.Context) (context.Context, uuid.UUID) {
	t.Helper()
	var id uuid.UUID
	email := "report-" + uuid.NewString()[:8] + "@example.test"
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.ports.emails[id] = email
	return authn.With(ctx, authn.Principal{UserID: id, SessionID: uuid.NewString(), Audience: authn.AudienceAdmin}), id
}

// newCommitPGEnv 构造支持真实提交的环境（并发用例专用）。
func newCommitPGEnv(t *testing.T) (*pgEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)

	reportTables := []string{"revenue_report_adjustments", "audit_events"}
	testkit.Truncate(t, d, reportTables...)
	t.Cleanup(func() { testkit.Truncate(t, d, reportTables...) })

	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x02
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	au, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	p := newFakePorts()
	deps := Deps{
		Settings: p, Users: p, Subscriptions: p, Nodes: p, Ledger: p, Orders: p, Payments: p,
		Commissions: p, Giftcards: p, Tickets: p, Notices: p, Activity: p, Redis: &fakeRedis{},
	}
	svc := New(repo.New(d), tx.NewManager(d), au, deps, Options{})

	ctx := context.Background()
	ctx = reqid.With(ctx, reqid.New())
	ctx = reqmeta.With(ctx, reqmeta.Meta{IP: "203.0.113.9", UserAgent: "report-test/1.0"})
	e := &pgEnv{
		svc:   svc,
		ports: p,
		row: func(ctx context.Context, q string, args ...any) *sql.Row {
			return d.Conn(ctx).Raw(q, args...).Row()
		},
		exec: func(ctx context.Context, q string, args ...any) error {
			return d.Conn(ctx).Exec(q, args...).Error
		},
	}
	return e, ctx
}
