package service

// 上线要求接入、服务器退役/删除吊销身份、节点列表附加读数的 PG 测试与夹具。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// invRandHex 返回 n 字节随机数的小写 hex。
func invRandHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// enroll 用 SQL 给服务器插一份未过期的 active 身份（夹具，不走接入流程）。
func (e *invEnv) enroll(t *testing.T, ctx context.Context, serverID uuid.UUID) {
	t.Helper()
	if err := e.exec(ctx, `INSERT INTO server_identities
		  (server_id, seq, cert_serial, spki_sha256, cert_pem, source, status, activated_at, issued_at, expires_at)
		SELECT ?, coalesce(max(seq), 0) + 1, ?, ?,
		       '-----BEGIN CERTIFICATE-----'||chr(10)||'ZmFrZQ=='||chr(10)||'-----END CERTIFICATE-----',
		       'renew', 'active', now(), now(), now() + interval '30 days'
		  FROM server_identities WHERE server_id = ?`,
		serverID, "a"+invRandHex(t, 15), invRandHex(t, 32), serverID); err != nil {
		t.Fatalf("插身份: %v", err)
	}
	if e.mr != nil {
		e.cleanupIdentityRows(t, serverID)
	}
}

// token 插一张未用、30 分钟后过期的接入令牌，返回 id。
func (e *invEnv) token(t *testing.T, ctx context.Context, serverID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO bootstrap_tokens (server_id, token_sha256, expires_at)
		SELECT ?, ?, now() + interval '30 minutes' RETURNING id`, serverID, invRandHex(t, 32)).Scan(&id); err != nil {
		t.Fatalf("插令牌: %v", err)
	}
	if e.mr != nil {
		e.cleanupIdentityRows(t, serverID)
	}
	return id
}

// cleanupIdentityRows 在真提交环境结束时删掉这台服务器的身份、令牌与指标（守卫触发器不许删，事务内临时 replica 跳过）。
// 后注册的 t.Cleanup 先执行，所以它在 invEnv 删服务器之前运行；同一台注册多次无害。
func (e *invEnv) cleanupIdentityRows(t *testing.T, serverID uuid.UUID) {
	t.Cleanup(func() {
		err := e.svc.tx.Do(context.Background(), func(ctx context.Context) error {
			if err := e.exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
				return err
			}
			for _, q := range []string{
				`DELETE FROM server_identities WHERE server_id = ?`,
				`DELETE FROM bootstrap_tokens WHERE server_id = ?`,
				`DELETE FROM node_metrics WHERE server_id = ?`,
			} {
				if err := e.exec(ctx, q, serverID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Errorf("清理身份: %v", err)
		}
	})
}

// invListCatalog 把每个被问到的池都当作已被套餐绑定（PlanNames 取 plans[池]，没给时 ["默认套餐"]）；unbound 里的池当作没绑。
type invListCatalog struct {
	fakePoolCatalog
	unbound map[uuid.UUID]bool
	plans   map[uuid.UUID][]string
}

func (c *invListCatalog) PoolUsage(_ context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]catalogsvc.PoolUsage, error) {
	out := map[uuid.UUID]catalogsvc.PoolUsage{}
	for _, id := range poolIDs {
		if c.unbound[id] {
			continue
		}
		names := c.plans[id]
		if names == nil {
			names = []string{"默认套餐"}
		}
		bindings := make([]catalogsvc.PoolBinding, 0, len(names))
		for _, name := range names {
			bindings = append(bindings, catalogsvc.PoolBinding{
				VersionID: uuid.New(),
				PlanName:  name,
				OnSale:    true,
			})
		}
		out[id] = catalogsvc.PoolUsage{
			Versions:  len(bindings),
			PlanNames: names,
			Bindings:  bindings,
		}
	}
	return out, nil
}

// invUseListCatalog 给 invEnv 的 service 装上 invListCatalog 并返回它。
func invUseListCatalog(e *invEnv) *invListCatalog {
	c := &invListCatalog{unbound: map[uuid.UUID]bool{}, plans: map[uuid.UUID][]string{}}
	e.svc.UseCatalog(c)
	e.svc.UseVersionsInUse(&fakeVersionsInUse{})
	return c
}

// newInvListEnv 是装好 invListCatalog 的回滚环境（节点列表要 catalog）。
func newInvListEnv(t *testing.T) (*invEnv, *invListCatalog, context.Context) {
	t.Helper()
	e, ctx := newInvEnv(t)
	return e, invUseListCatalog(e), ctx
}

// invScalar 读一个标量。
func invScalar[T any](t *testing.T, e *invEnv, ctx context.Context, q string, args ...any) T {
	t.Helper()
	var v T
	if err := e.row(ctx, q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

func TestActivateNodeRequiresEnrollmentPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "enr-a")
	n := e.ssNode(t, ctx, srv.ID, "n", 23001)

	_, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_id"] != serverNotEnrolledMsg {
		t.Fatalf("没接入应报 server_id：%v", ee.Fields)
	}
	if st := invScalar[string](t, e, ctx, `SELECT serving_status FROM nodes WHERE id = ?`, n.ID); st != NodeDraft {
		t.Fatalf("节点不应改动：%s", st)
	}
	if st := invScalar[string](t, e, ctx, `SELECT status FROM servers WHERE id = ?`, srv.ID); st != ServerDraft {
		t.Fatalf("服务器不应改动：%s", st)
	}

	e.enroll(t, ctx, srv.ID)
	got, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServingStatus != NodeActive {
		t.Fatalf("接入后应能上线：%s", got.ServingStatus)
	}
	if st := invScalar[string](t, e, ctx, `SELECT status FROM servers WHERE id = ?`, srv.ID); st != ServerReady {
		t.Fatalf("服务器应转 ready：%s", st)
	}
}

func TestBatchActivateRequiresEnrollmentPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "enr-b")
	n := e.ssNode(t, ctx, srv.ID, "n", 23002)
	batch := NodeStatusBatch{ServingStatus: NodeActive, Items: []NodeVersionRef{{ID: n.ID, RowVersion: n.RowVersion}}}

	ee := invWantCode(t, e.svc.BatchNodeStatus(ctx, batch), errs.Conflict)
	if ee.Fields["server_id"] != serverNotEnrolledMsg || ee.Fields["id"] != n.ID.String() {
		t.Fatalf("没接入应报 server_id 与 id：%v", ee.Fields)
	}
	e.enroll(t, ctx, srv.ID)
	if err := e.svc.BatchNodeStatus(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if st, _ := invServingOf(t, e, ctx, n.ID); st != NodeActive {
		t.Fatalf("接入后批量启用应成功：%s", st)
	}
}

// invCheckRevoked 核对服务器退役/删除后的身份、令牌、Tombstone 与审计。
func invCheckRevoked(t *testing.T, e *invEnv, ctx context.Context, tombs *fakeTombstones,
	serverID, tokenID uuid.UUID, reason, tombstone, action string) {
	t.Helper()
	n := invScalar[int64](t, e, ctx, `SELECT count(*) FROM server_identities
		WHERE server_id = ? AND status = 'revoked' AND revoked_reason = ?`, serverID, reason)
	if n != 1 {
		t.Fatalf("身份应以「%s」吊销：%d 行", reason, n)
	}
	if !invScalar[bool](t, e, ctx, `SELECT voided_at IS NOT NULL FROM bootstrap_tokens WHERE id = ?`, tokenID) {
		t.Fatal("未用令牌应作废")
	}
	if got := tombs.list(); !slices.Equal(got, []string{serverID.String() + ":" + tombstone}) {
		t.Fatalf("提交后应发一次 Tombstone：%v", got)
	}
	voided := invScalar[string](t, e, ctx, `SELECT data->>'voided_tokens' FROM audit_events
		WHERE action = ? AND resource_id = ? ORDER BY occurred_at DESC LIMIT 1`, action, serverID)
	ids := invScalar[int64](t, e, ctx, `SELECT jsonb_array_length(data->'revoked_identity_ids') FROM audit_events
		WHERE action = ? AND resource_id = ? ORDER BY occurred_at DESC LIMIT 1`, action, serverID)
	if voided != "1" || ids != 1 {
		t.Fatalf("审计应记作废令牌数与吊销的身份：voided=%s ids=%d", voided, ids)
	}
}

func TestRetireServerRevokesIdentityPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	tombs := &fakeTombstones{}
	e.svc.UseSessions(tombs)
	srv := e.server(t, ctx, "enr-ret")
	e.enroll(t, ctx, srv.ID)
	tok := e.token(t, ctx, srv.ID)

	if _, err := e.svc.SetServerStatus(ctx, srv.ID, srv.RowVersion, ServerRetired, "r"); err != nil {
		t.Fatal(err)
	}
	invCheckRevoked(t, e, ctx, tombs, srv.ID, tok, identityRevokeReasonRetired, TombstoneServerRetired, "server.status_change")
}

func TestDeleteServerRevokesIdentityPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	tombs := &fakeTombstones{}
	e.svc.UseSessions(tombs)
	srv := e.server(t, ctx, "enr-del")
	e.enroll(t, ctx, srv.ID)
	tok := e.token(t, ctx, srv.ID)

	if err := e.svc.DeleteServer(ctx, srv.ID, srv.RowVersion); err != nil {
		t.Fatal(err)
	}
	invCheckRevoked(t, e, ctx, tombs, srv.ID, tok, identityRevokeReasonDeleted, TombstoneServerDeleted, "server.delete")
}

func TestRetireServerFailedKeepsIdentityPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "enr-keep")
	e.enroll(t, ctx, srv.ID)
	_, err := e.svc.SetServerStatus(ctx, srv.ID, srv.RowVersion, ServerRetired, "")
	_ = invWantCode(t, err, errs.Conflict)
	n := invScalar[int64](t, e, ctx, `SELECT count(*) FROM server_identities WHERE server_id = ? AND status = 'active'`, srv.ID)
	if n != 1 {
		t.Fatalf("退役失败不应吊销身份：active %d 行", n)
	}
}

func TestQueryNodesHookFieldsPG(t *testing.T) {
	e, cat, ctx := newInvListEnv(t)
	srv, n := invPooledActive(t, e, ctx, "hook-srv", "hook", 23011)
	if n.PoolID == nil {
		t.Fatal("夹具节点应划了池")
	}
	pool := *n.PoolID
	cat.plans[pool] = []string{"甲", "乙"}

	if err := e.svc.repo.InsertMetric(ctx, repo.MetricRow{
		ServerID: srv.ID, RecordedAt: time.Now().UTC().Truncate(time.Microsecond), CPUBp: 1234,
		MemUsedBytes: 512 << 20, MemTotalBytes: 1 << 30, DiskUsedBytes: 10 << 30, DiskTotalBytes: 40 << 30,
	}); err != nil {
		t.Fatal(err)
	}

	r := invFindRow(t, e, ctx, n.ID)
	if r.CPUPercent == nil || *r.CPUPercent != 12.34 || r.MemPercent == nil || *r.MemPercent != 50 || r.MetricsAt == nil {
		t.Fatalf("指标不对：cpu=%v mem=%v at=%v", r.CPUPercent, r.MemPercent, r.MetricsAt)
	}
	if r.IdentitySerial == nil || *r.IdentitySerial != 1 {
		t.Fatalf("身份序号应为 1：%v", r.IdentitySerial)
	}
	// 套餐名按字节序升序：乙 U+4E59 在甲 U+7532 前
	if !slices.Equal(r.GrantedPlans, []string{"乙", "甲"}) || r.OnlineUsers != 0 || r.OnlineIPs != 0 || r.TrafficBytes24h != 0 {
		t.Fatalf("套餐或在线不对：%v %d %d %d", r.GrantedPlans, r.OnlineUsers, r.OnlineIPs, r.TrafficBytes24h)
	}
	if !r.Delivered || r.EffectiveState != "pending" {
		t.Fatalf("新上线还没应用应为 pending 且下发：%v %q", r.Delivered, r.EffectiveState)
	}

	docs, err := e.svc.ServerConfigs(ctx, []uuid.UUID{srv.ID})
	if err != nil {
		t.Fatal(err)
	}
	sha := ""
	for _, d := range docs[srv.ID] {
		if d.NodeID == n.ID {
			sha = d.SHA256
		}
	}
	if sha == "" {
		t.Fatal("在役节点应有配置文档")
	}
	if err := e.exec(ctx, `UPDATE nodes SET applied_config_sha256 = ?, applied_config_at = now() WHERE id = ?`, sha, n.ID); err != nil {
		t.Fatal(err)
	}
	if r := invFindRow(t, e, ctx, n.ID); r.EffectiveState != "applied" {
		t.Fatalf("应用了当前文档应为 applied：%q", r.EffectiveState)
	}

	s, err := e.svc.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUBp == nil || *s.CPUBp != 1234 || s.MemUsedMB == nil || *s.MemUsedMB != 512 || s.MemTotalMB == nil || *s.MemTotalMB != 1024 ||
		s.DiskUsedGB == nil || *s.DiskUsedGB != 10 || s.DiskTotalGB == nil || *s.DiskTotalGB != 40 || s.MetricsAt == nil {
		t.Fatalf("服务器指标不对：%+v", s)
	}

	cat.unbound[pool] = true
	r = invFindRow(t, e, ctx, n.ID)
	if r.Delivered || r.DeliveryNote != nodeNotePoolUnbound || r.GrantedPlans == nil || len(r.GrantedPlans) != 0 {
		t.Fatalf("池没绑套餐应不下发：%v %q %v", r.Delivered, r.DeliveryNote, r.GrantedPlans)
	}
}

func TestQueryNodesNeedsCatalogPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	_, _, err := e.svc.QueryNodes(ctx, NodeQuery{Limit: 10})
	_ = invWantCode(t, err, errs.Unavailable)
}
