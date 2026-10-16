package service

// 节点池 PG 测试的环境、假端口与夹具。catalog、identity 用假实现（各自的行为在它们模块里测），
// 端到端的发布检查另用真 catalog（plan_pools_e2e_pg_test.go）。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakePoolCatalog 是内存里的 Catalog：套餐绑池、池用量、套餐都由用例直接填。
type fakePoolCatalog struct {
	planPools    map[uuid.UUID][]uuid.UUID
	usage        map[uuid.UUID]catalogsvc.PoolUsage
	plans        map[uuid.UUID]catalogsvc.Plan
	released     map[uuid.UUID][]uuid.UUID
	releaseCalls []uuid.UUID
}

func (f *fakePoolCatalog) PlanPools(_ context.Context, planIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	out := map[uuid.UUID][]uuid.UUID{}
	for _, id := range planIDs {
		if p, ok := f.planPools[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func (f *fakePoolCatalog) PoolUsage(_ context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]catalogsvc.PoolUsage, error) {
	out := map[uuid.UUID]catalogsvc.PoolUsage{}
	for _, id := range poolIDs {
		if u, ok := f.usage[id]; ok {
			if u.Bindings == nil {
				bindings := make([]catalogsvc.PoolBinding, 0, len(u.PlanNames))
				for _, name := range u.PlanNames {
					bindings = append(bindings, catalogsvc.PoolBinding{
						PlanID:     uuid.New(),
						PlanName:   name,
						OnSale:     true,
						VersionIDs: []uuid.UUID{uuid.New()},
					})
				}
				u.Bindings = bindings
			}
			out[id] = u
		}
	}
	return out, nil
}

func (f *fakePoolCatalog) PlansByIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error) {
	out := map[uuid.UUID]catalogsvc.Plan{}
	for _, id := range ids {
		if p, ok := f.plans[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func (f *fakePoolCatalog) ReleaseArchivedPoolBindings(_ context.Context, poolID uuid.UUID) ([]uuid.UUID, error) {
	f.releaseCalls = append(f.releaseCalls, poolID)
	res := f.released[poolID]
	if res == nil {
		return []uuid.UUID{}, nil
	}
	return res, nil
}

// fakeVersionsInUse 是 VersionsInUse 端口的假实现：默认返回空 map。
type fakeVersionsInUse struct {
	inUse map[uuid.UUID]bool
	err   error
}

func (f *fakeVersionsInUse) VersionsInUse(_ context.Context, _ []uuid.UUID) (map[uuid.UUID]bool, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.inUse == nil {
		return map[uuid.UUID]bool{}, nil
	}
	return f.inUse, nil
}

// fakePoolGroups 是内存里的 UserGroups；seedPoolGroup 建组时同步追加。
type fakePoolGroups struct{ groups []identitysvc.Group }

func (f *fakePoolGroups) ListGroups(context.Context) ([]identitysvc.Group, error) {
	return append([]identitysvc.Group(nil), f.groups...), nil
}

type poolEnv struct {
	svc    *Service
	cat    *fakePoolCatalog
	groups *fakePoolGroups
	mr     *miniredis.Miniredis
	exec   func(ctx context.Context, q string, args ...any) error
	row    func(ctx context.Context, q string, args ...any) *sql.Row
	// 真提交模式下记下本用例建过的行，用例结束时删掉。
	committed bool
	pools     []uuid.UUID
	groupIDs  []uuid.UUID
	nodes     []uuid.UUID
	servers   []uuid.UUID
}

// newPoolEnv 建一个跑在回滚事务里的环境（提交后钩子不执行）。
func newPoolEnv(t *testing.T) (*poolEnv, context.Context) {
	return newPoolEnvMode(t, false)
}

// newPoolEnvCommitted 建一个真提交的环境：MarkUsersChanged 的提交后钩子会推进 miniredis 里的纪元。
func newPoolEnvCommitted(t *testing.T) (*poolEnv, context.Context) {
	return newPoolEnvMode(t, true)
}

func newPoolEnvMode(t *testing.T, committed bool) (*poolEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	mr, rdb := testkit.Redis(t)
	svc := New(repo.New(d), tx.NewManager(d), auditSvc, rdb)
	cat := &fakePoolCatalog{
		planPools: map[uuid.UUID][]uuid.UUID{},
		usage:     map[uuid.UUID]catalogsvc.PoolUsage{},
		plans:     map[uuid.UUID]catalogsvc.Plan{},
	}
	groups := &fakePoolGroups{}
	svc.UseCatalog(cat)
	svc.UseUserGroups(groups)
	svc.UseVersionsInUse(&fakeVersionsInUse{})

	e := &poolEnv{
		svc: svc, cat: cat, groups: groups, mr: mr, committed: committed,
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	ctx := context.Background()
	if !committed {
		ctx = testkit.TxCtx(t, d)
	} else {
		t.Cleanup(func() { e.cleanupCommitted(t) })
	}
	ctx = reqid.With(ctx, reqid.New())

	var adminID uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, poolRandName(t, "admin")+"@example.test").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	ctx = authn.With(ctx, authn.Principal{UserID: adminID, SessionID: "s-admin", Audience: authn.AudienceAdmin})
	return e, ctx
}

// cleanupCommitted 删掉真提交模式下建过的行（审计与用户行不删）。
func (e *poolEnv) cleanupCommitted(t *testing.T) {
	ctx := context.Background()
	for _, q := range []struct {
		sql string
		ids []uuid.UUID
	}{
		{`DELETE FROM node_pool_user_groups WHERE pool_id IN ?`, e.pools},
		{`DELETE FROM nodes WHERE id IN ?`, e.nodes},
		{`DELETE FROM node_pools WHERE id IN ?`, e.pools},
		{`DELETE FROM servers WHERE id IN ?`, e.servers},
		{`DELETE FROM user_groups WHERE id IN ?`, e.groupIDs},
	} {
		if len(q.ids) == 0 {
			continue
		}
		if err := e.exec(ctx, q.sql, q.ids); err != nil {
			t.Errorf("清理: %v", err)
		}
	}
}

// trackPool 记下用例通过 service 建的池（真提交模式下结束时删）。
func (e *poolEnv) trackPool(id uuid.UUID) { e.pools = append(e.pools, id) }

func poolRandName(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s%s", prefix, hex.EncodeToString(b))
}

// seedPoolGroup 建一个用户组并加进假 identity。
func seedPoolGroup(t *testing.T, ctx context.Context, e *poolEnv, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO user_groups (code, name) VALUES (?, ?) RETURNING id`, poolRandName(t, "g"), name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.groups.groups = append(e.groups.groups, identitysvc.Group{ID: id, Name: name})
	e.groupIDs = append(e.groupIDs, id)
	return id
}

// seedPoolRow 直接插一个节点池。
func seedPoolRow(t *testing.T, ctx context.Context, e *poolEnv, status string) uuid.UUID {
	t.Helper()
	code := poolRandName(t, "p")
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO node_pools (code, name, status) VALUES (?, ?, ?) RETURNING id`, code, "池 "+code, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.pools = append(e.pools, id)
	return id
}

// seedPoolServer 插一台服务器；heartbeat 为真时 last_heartbeat_at = now()。
func seedPoolServer(t *testing.T, ctx context.Context, e *poolEnv, status string, heartbeat bool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO servers (name, status, last_heartbeat_at, retired_at)
		VALUES (?, ?, CASE WHEN ? THEN now() END, CASE WHEN ? = 'retired' THEN now() END) RETURNING id`,
		poolRandName(t, "srv"), status, heartbeat, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.servers = append(e.servers, id)
	return id
}

// seedPoolNode 插一个节点：active 时写全协议列（nodes_active_shape），draft 只写名字与归属，retired 带 retired_at。
func seedPoolNode(t *testing.T, ctx context.Context, e *poolEnv, poolID uuid.UUID, serverID *uuid.UUID, serving string, port int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	var err error
	switch serving {
	case "active", "disabled":
		err = e.row(ctx, `INSERT INTO nodes (name, pool_id, server_id, serving_status, node_type, server_host,
			server_port, listen_l4, protocol_schema_version)
			VALUES (?, ?, ?, ?, 'vless', 'node.example.test', ?, 'tcp', 1) RETURNING id`,
			poolRandName(t, "n"), poolID, serverID, serving, port).Scan(&id)
	case "retired":
		err = e.row(ctx, `INSERT INTO nodes (name, pool_id, server_id, serving_status, retired_at)
			VALUES (?, ?, ?, 'retired', now()) RETURNING id`, poolRandName(t, "n"), poolID, serverID).Scan(&id)
	default:
		err = e.row(ctx, `INSERT INTO nodes (name, pool_id, server_id) VALUES (?, ?, ?) RETURNING id`,
			poolRandName(t, "n"), poolID, serverID).Scan(&id)
	}
	if err != nil {
		t.Fatal(err)
	}
	e.nodes = append(e.nodes, id)
	return id
}

// deletePoolNode 把节点软删（先退役，nodes_deleted_shape 要求删掉的必须是 retired）。
func deletePoolNode(t *testing.T, ctx context.Context, e *poolEnv, nodeID uuid.UUID) {
	t.Helper()
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'retired', retired_at = coalesce(retired_at, now()),
		deleted_at = now() WHERE id = ?`, nodeID); err != nil {
		t.Fatal(err)
	}
}

// poolEpoch 读名单纪元（没有返回 ""）。
func poolEpoch(t *testing.T, e *poolEnv) string {
	t.Helper()
	if !e.mr.Exists(UsersEpochKey) {
		return ""
	}
	v, err := e.mr.Get(UsersEpochKey)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// poolAuditCount 数某个资源的某种审计。
func poolAuditCount(t *testing.T, ctx context.Context, e *poolEnv, action string, id uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, action, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// poolSavepoint 在 SAVEPOINT 里调 fn，出错回滚到保存点，让回滚事务里的用例能继续。
func poolSavepoint(t *testing.T, ctx context.Context, e *poolEnv, fn func() error) error {
	t.Helper()
	if e.committed {
		return fn()
	}
	if err := e.exec(ctx, "SAVEPOINT pool_sp"); err != nil {
		t.Fatal(err)
	}
	err := fn()
	if err != nil {
		if rerr := e.exec(ctx, "ROLLBACK TO SAVEPOINT pool_sp"); rerr != nil {
			t.Fatal(rerr)
		}
		return err
	}
	if rerr := e.exec(ctx, "RELEASE SAVEPOINT pool_sp"); rerr != nil {
		t.Fatal(rerr)
	}
	return nil
}

func poolErrCode(err error) errs.Code {
	if err == nil {
		return ""
	}
	return errs.As(err).Code
}

func poolErrFields(err error) map[string]string {
	if err == nil {
		return nil
	}
	return errs.As(err).Fields
}
