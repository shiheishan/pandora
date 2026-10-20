package service

// 服务器与节点 PG 测试的夹具。没有 PANDORA_TEST_DATABASE_URL 时 testkit.DB 自动跳过。
// 回滚环境（newInvEnv）里提交后钩子不执行；要核对纪元通知用真提交环境（newInvEnvCommitted），
// 它按每个用例唯一的 tag 在结束时删掉本用例建的节点、服务器与节点池。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// 测试用协议配置：TCP 一份、UDP 一份、UDP 带密钥一份。
const (
	invSSConfig       = `{"cipher":"aes-256-gcm"}`
	invHy2Config      = `{"cert_path":"/etc/pandora-native/certs/t.pem","key_path":"/etc/pandora-native/certs/t.key"}`
	invHy2Secret      = "s3cret"
	invHy2SecretField = `{"cert_path":"/etc/pandora-native/certs/t.pem","key_path":"/etc/pandora-native/certs/t.key","obfs":{"type":"salamander","password":"s3cret"}}`
)

type invEnv struct {
	svc  *Service
	exec func(ctx context.Context, sql string, args ...any) error
	row  func(ctx context.Context, sql string, args ...any) *sql.Row
	// mr 只在真提交环境非 nil
	mr *miniredis.Miniredis
	// tag 是每个用例唯一的名字后缀，夹具名字都带它
	tag   string
	pools int
}

func newInvEnv(t *testing.T) (*invEnv, context.Context) { return newInvEnvMode(t, false) }

func newInvEnvCommitted(t *testing.T) (*invEnv, context.Context) { return newInvEnvMode(t, true) }

func newInvEnvMode(t *testing.T, committed bool) (*invEnv, context.Context) {
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
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	e := &invEnv{
		tag:  hex.EncodeToString(b[:]),
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	ctx := context.Background()
	if committed {
		mr, rdb := testkit.Redis(t)
		e.mr = mr
		e.svc = New(repo.New(d), tx.NewManager(d), auditSvc, rdb)
		t.Cleanup(func() { e.cleanup(t) })
	} else {
		e.svc = New(repo.New(d), tx.NewManager(d), auditSvc, nil)
		ctx = testkit.TxCtx(t, d)
	}
	// Xboard 补齐的两个端口：站点时区（流量周期、时段倍率）、ECH 私钥封存（假封存器，密文里带 AAD 便于断言）
	e.svc.UseSiteSettings(fakeSite{tz: "UTC"})
	e.svc.UseECHSealer(fakeSealer{})
	return e, reqid.With(ctx, reqid.New())
}

// cleanup 删掉真提交环境里本用例建的行（审计行不删）。
func (e *invEnv) cleanup(t *testing.T) {
	ctx := context.Background()
	like := "%" + e.tag + "%"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM nodes WHERE name LIKE ? OR server_id IN (SELECT id FROM servers WHERE name LIKE ?)`, []any{like, like}},
		{`DELETE FROM servers WHERE name LIKE ?`, []any{like}},
		{`DELETE FROM node_pools WHERE code LIKE ?`, []any{like}},
	} {
		if err := e.exec(ctx, q.sql, q.args...); err != nil {
			t.Errorf("清理: %v", err)
		}
	}
}

// name 给名字加上本用例的 tag。
func (e *invEnv) name(base string) string { return base + "-" + e.tag }

// server 建一台草稿服务器。
func (e *invEnv) server(t *testing.T, ctx context.Context, name string) Server {
	t.Helper()
	s, err := e.svc.CreateServer(ctx, ServerInput{Name: e.name(name), PublicIPv4: "203.0.113.10"})
	if err != nil {
		t.Fatalf("建服务器: %v", err)
	}
	return s
}

// readyServer 建一台服务器并用 SQL 置为 ready、刚有心跳。
func (e *invEnv) readyServer(t *testing.T, ctx context.Context, name string) Server {
	t.Helper()
	s := e.server(t, ctx, name)
	if err := e.exec(ctx, `UPDATE servers SET status = 'ready', last_heartbeat_at = now() WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetServer(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// pool 用 SQL 建一个节点池。
func (e *invEnv) pool(t *testing.T, ctx context.Context, status string) uuid.UUID {
	t.Helper()
	e.pools++
	code := "p" + e.tag + strconv.Itoa(e.pools)
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO node_pools (code, name, status) VALUES (?, ?, ?) RETURNING id`,
		code, code, status).Scan(&id); err != nil {
		t.Fatalf("建节点池: %v", err)
	}
	return id
}

// ssNode 建一个 shadowsocks（TCP）草稿节点。
func (e *invEnv) ssNode(t *testing.T, ctx context.Context, serverID uuid.UUID, name string, port int) AdminNode {
	t.Helper()
	return e.node(t, ctx, serverID, name, "shadowsocks", port, invSSConfig)
}

// hy2Node 建一个 hysteria2（UDP）草稿节点。
func (e *invEnv) hy2Node(t *testing.T, ctx context.Context, serverID uuid.UUID, name string, port int) AdminNode {
	t.Helper()
	return e.node(t, ctx, serverID, name, "hysteria2", port, invHy2Config)
}

func (e *invEnv) node(t *testing.T, ctx context.Context, serverID uuid.UUID, name, nodeType string, port int, cfg string) AdminNode {
	t.Helper()
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name(name), ServerID: &serverID, NodeType: nodeType, ServerHost: "203.0.113.10",
		ServerPort: port, ProtocolConfig: json.RawMessage(cfg),
	})
	if err != nil {
		t.Fatalf("建节点 %s: %v", name, err)
	}
	return n
}

// epoch 读 miniredis 里的纪元（UsersEpochKey / ConfigEpochKey），没有为 0。
func (e *invEnv) epoch(t *testing.T, key string) int64 {
	t.Helper()
	if e.mr == nil {
		t.Fatal("epoch 只能在真提交环境里用")
	}
	if !e.mr.Exists(key) {
		return 0
	}
	v, err := e.mr.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// invWantCode 取 *errs.Error 并核对错误码。
func invWantCode(t *testing.T, err error, code errs.Code) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("期望 %s，得到 %v", code, err)
	}
	if e.Code != code {
		t.Fatalf("错误码 %s，期望 %s：%v（%v）", e.Code, code, e.Message, e.Fields)
	}
	return e
}

// invSavepoint 在回滚环境里用保存点包住一次会撞库约束的调用：出错时回到保存点，外层事务还能接着用。
// 真提交环境不需要（每次调用自己的事务）。
func (e *invEnv) invSavepoint(t *testing.T, ctx context.Context, fn func() error) error {
	t.Helper()
	if e.mr != nil {
		return fn()
	}
	if err := e.exec(ctx, `SAVEPOINT inv_sp`); err != nil {
		t.Fatal(err)
	}
	err := fn()
	q := `RELEASE SAVEPOINT inv_sp`
	if err != nil {
		q = `ROLLBACK TO SAVEPOINT inv_sp`
	}
	if xerr := e.exec(ctx, q); xerr != nil {
		t.Fatal(xerr)
	}
	return err
}
