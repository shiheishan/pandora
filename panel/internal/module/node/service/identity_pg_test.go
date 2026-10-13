package service

// 节点身份的 PG 测试：环境与夹具、两步续期、吊销、认证材料守卫、CA 加载。接入（Enroll）的用例在 enroll_pg_test.go。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// fakeTombstones 记下会话层收到的 Tombstone（服务器 id:原因）。
type fakeTombstones struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeTombstones) Tombstone(serverID uuid.UUID, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, serverID.String()+":"+reason)
}

func (f *fakeTombstones) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type identEnv struct {
	svc   *Service
	ca    *CA
	keys  *crypto.Keys
	tombs *fakeTombstones
	txm   *tx.Manager
	exec  func(ctx context.Context, q string, args ...any) error
	row   func(ctx context.Context, q string, args ...any) *sql.Row
	// admin 是带管理员身份的 ctx（签令牌、吊销用）；接入与建流用不带身份的 ctx
	admin     context.Context
	committed bool
	servers   []uuid.UUID
}

func identMasterKeys(t *testing.T, b byte) *crypto.Keys {
	t.Helper()
	master := make([]byte, 32)
	for i := range master {
		master[i] = b
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// newIdentEnv 建环境：committed=false 时跑在回滚事务里（提交后钩子不执行），true 时真提交、结束时清掉建过的行。
func newIdentEnv(t *testing.T, committed bool) (*identEnv, context.Context) {
	t.Helper()
	d := testkit.DB(t)
	keys := identMasterKeys(t, 0x01)
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	txm := tx.NewManager(d)
	svc := New(repo.New(d), txm, auditSvc, nil)
	ca, _, err := generateCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	svc.UseEnrollment(ca, InstallTarget{BaseURL: "https://panel.example.test/", Host: "node.example.test", Port: 9443})
	tombs := &fakeTombstones{}
	svc.UseSessions(tombs)

	e := &identEnv{
		svc: svc, ca: ca, keys: keys, tombs: tombs, txm: txm, committed: committed,
		exec: func(ctx context.Context, q string, args ...any) error { return d.Conn(ctx).Exec(q, args...).Error },
		row:  func(ctx context.Context, q string, args ...any) *sql.Row { return d.Conn(ctx).Raw(q, args...).Row() },
	}
	ctx := context.Background()
	if committed {
		t.Cleanup(func() { e.cleanup(t) })
	} else {
		ctx = testkit.TxCtx(t, d)
	}
	ctx = reqid.With(ctx, reqid.New())

	var adminID uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`, poolRandName(t, "admin")+"@example.test").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	e.admin = authn.With(ctx, authn.Principal{UserID: adminID, SessionID: "s-admin", Audience: authn.AudienceAdmin})
	return e, ctx
}

// cleanup 删掉真提交模式下建过的服务器及其令牌与身份。身份表不许删（守卫触发器），
// 这里在事务内临时 session_replication_role = replica 跳过（测试库用超级用户连，与 testkit.Truncate 同）。
func (e *identEnv) cleanup(t *testing.T) {
	if len(e.servers) == 0 {
		return
	}
	err := e.txm.Do(context.Background(), func(ctx context.Context) error {
		for _, q := range []string{
			`SET LOCAL session_replication_role = replica`,
			`DELETE FROM nodes WHERE server_id IN ?`,
			`DELETE FROM server_identities WHERE server_id IN ?`,
			`DELETE FROM bootstrap_tokens WHERE server_id IN ?`,
			`DELETE FROM servers WHERE id IN ?`,
		} {
			var args []any
			if q[0] == 'D' {
				args = []any{e.servers}
			}
			if err := e.exec(ctx, q, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Errorf("清理: %v", err)
	}
}

// seedIdentServer 插一台服务器（status 为 retired 时带 retired_at）。
func seedIdentServer(t *testing.T, ctx context.Context, e *identEnv, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO servers (name, status, retired_at)
		VALUES (?, ?, CASE WHEN ? = 'retired' THEN now() END) RETURNING id`,
		poolRandName(t, "srv"), status, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	e.servers = append(e.servers, id)
	return id
}

// seedIdentNode 在服务器上插一个草稿节点。
func seedIdentNode(t *testing.T, ctx context.Context, e *identEnv, serverID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := e.row(ctx, `INSERT INTO nodes (name, server_id) VALUES (?, ?) RETURNING id`,
		poolRandName(t, "n"), serverID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func identKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// identCSR 用 key 签一份 CSR；每次调用字节都不同（ECDSA 签名带随机数）。
func identCSR(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "node"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// identIssueToken 以管理员身份签一张令牌，返回明文。
func identIssueToken(t *testing.T, e *identEnv, serverID uuid.UUID) string {
	t.Helper()
	out, err := e.svc.IssueBootstrapToken(e.admin, serverID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return out.Token
}

// identLeaf 取 PEM 链里的叶子证书。
func identLeaf(t *testing.T, chain []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(chain)
	if b == nil {
		t.Fatal("链里没有 PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// identEnroll 签令牌并接入，返回叶子证书的序列号。
func identEnroll(t *testing.T, ctx context.Context, e *identEnv, serverID uuid.UUID) *big.Int {
	t.Helper()
	res, err := e.svc.Enroll(ctx, EnrollInput{
		Token: identIssueToken(t, e, serverID), CSRDER: identCSR(t, identKey(t)), RequestID: reqid.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return identLeaf(t, res.ChainPEM).SerialNumber
}

// identRenew 续期，返回新证书的序列号。
func identRenew(t *testing.T, ctx context.Context, e *identEnv, serverID uuid.UUID) *big.Int {
	t.Helper()
	chain, caPEM, err := e.svc.RenewCertificate(ctx, serverID, identCSR(t, identKey(t)))
	if err != nil {
		t.Fatal(err)
	}
	if string(caPEM) != string(e.ca.CertPEM()) {
		t.Fatal("续期返回的 CA 不对")
	}
	return identLeaf(t, chain).SerialNumber
}

// identStatus 读某个序列号的身份状态。
func identStatus(t *testing.T, ctx context.Context, e *identEnv, serial *big.Int) string {
	t.Helper()
	var st string
	if err := e.row(ctx, `SELECT status FROM server_identities WHERE cert_serial = ?`, serial.Text(16)).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func identAuditCount(t *testing.T, ctx context.Context, e *identEnv, action string, serverID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ?`, action, serverID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// identSavepoint 在 SAVEPOINT 里执行一条预期失败的 SQL，失败后回滚到保存点，返回那条 SQL 的错误。
func identSavepoint(t *testing.T, ctx context.Context, e *identEnv, q string, args ...any) error {
	t.Helper()
	if err := e.exec(ctx, "SAVEPOINT ident_sp"); err != nil {
		t.Fatal(err)
	}
	err := e.exec(ctx, q, args...)
	next := "RELEASE SAVEPOINT ident_sp"
	if err != nil {
		next = "ROLLBACK TO SAVEPOINT ident_sp"
	}
	if rerr := e.exec(ctx, next); rerr != nil {
		t.Fatal(rerr)
	}
	return err
}

func wantSessionRevoked(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("%s：应回 ErrSessionRevoked，得到 %v", what, err)
	}
}

func TestRenewTwoStepPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)

	// 续期：新证书 pending，旧证书照常可用
	b := identRenew(t, ctx, e, sid)
	if got := identStatus(t, ctx, e, b); got != "pending" {
		t.Fatalf("续期证书应为 pending，得到 %s", got)
	}
	if err := e.svc.AuthorizeSession(ctx, sid, a); err != nil {
		t.Fatalf("续期后旧证书应仍可建流：%v", err)
	}
	if got := identStatus(t, ctx, e, a); got != "active" {
		t.Fatalf("旧证书应仍 active，得到 %s", got)
	}

	// 再次续期：没用过的 pending 被取代，旧 active 仍有效
	c := identRenew(t, ctx, e, sid)
	if got := identStatus(t, ctx, e, b); got != "superseded" {
		t.Fatalf("没用过的 pending 应被取代，得到 %s", got)
	}
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, b), "被取代的 pending")
	if err := e.svc.AuthorizeSession(ctx, sid, a); err != nil {
		t.Fatalf("旧证书应仍可建流：%v", err)
	}

	// 节点第一次用新证书建流：pending 转正，旧的作废
	if err := e.svc.AuthorizeSession(ctx, sid, c); err != nil {
		t.Fatalf("pending 证书建流应通过：%v", err)
	}
	if got := identStatus(t, ctx, e, c); got != "active" {
		t.Fatalf("pending 第一次建流后应 active，得到 %s", got)
	}
	if got := identStatus(t, ctx, e, a); got != "superseded" {
		t.Fatalf("旧证书应被取代，得到 %s", got)
	}
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, a), "转正后的旧证书")
	if err := e.svc.AuthorizeSession(ctx, sid, c); err != nil {
		t.Fatalf("转正后再次建流应通过：%v", err)
	}
	if n := identAuditCount(t, ctx, e, "server.identity.renew", sid); n != 2 {
		t.Fatalf("续期审计应 2 条，得到 %d", n)
	}
	if n := identAuditCount(t, ctx, e, "server.identity.activate", sid); n != 1 {
		t.Fatalf("转正审计应 1 条，得到 %d", n)
	}
	if ok, err := e.svc.HasActiveIdentity(ctx, sid); err != nil || !ok {
		t.Fatalf("HasActiveIdentity = %v, %v", ok, err)
	}
}

func TestRenewRequiresActiveIdentityPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	if _, _, err := e.svc.RenewCertificate(ctx, sid, identCSR(t, identKey(t))); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("没有有效身份时续期应回 ErrSessionRevoked，得到 %v", err)
	}
	if _, _, err := e.svc.RenewCertificate(ctx, uuid.New(), identCSR(t, identKey(t))); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("服务器不存在时续期应回 ErrSessionRevoked，得到 %v", err)
	}
	identEnroll(t, ctx, e, sid)
	if _, _, err := e.svc.RenewCertificate(ctx, sid, []byte("bad")); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("坏 CSR 应回 errCSRInvalid，得到 %v", err)
	}
}

func TestAuthorizeSessionRejectsPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, uuid.New(), a), "别的服务器 id")
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, big.NewInt(12345)), "陌生序列号")
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, nil), "空序列号")

	// 证书过期：把时钟拨到 31 天后
	e.svc.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, a), "过期证书")
	e.svc.now = time.Now

	// 服务器软删（先退役）
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now(), deleted_at = now() WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, a), "已删除的服务器")
}

func TestRevokeNodeIdentityPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)
	a := identEnroll(t, ctx, e, sid)
	b := identRenew(t, ctx, e, sid)

	// 后台展示的是 active 那份，不是 pending
	view, err := e.svc.NodeIdentity(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if view.Identity == nil || view.Identity.Seq != 1 || view.Identity.Status != "active" {
		t.Fatalf("应展示 seq 1 的 active 身份：%+v", view.Identity)
	}

	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*big.Int{a, b} {
		if got := identStatus(t, ctx, e, s); got != "revoked" {
			t.Fatalf("active 与 pending 都应吊销，%s 得到 %s", s.Text(16), got)
		}
		wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, s), "吊销后建流")
	}
	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); poolErrCode(err) != errs.Conflict {
		t.Fatalf("再次吊销应 409，得到 %v", err)
	}
	if n := identAuditCount(t, ctx, e, "node.identity.revoke", sid); n != 1 {
		t.Fatalf("吊销审计应 1 条，得到 %d", n)
	}
	if err := e.svc.RevokeNodeIdentity(e.admin, uuid.New()); poolErrCode(err) != errs.NotFound {
		t.Fatalf("节点不存在应 404，得到 %v", err)
	}
	if ok, err := e.svc.HasActiveIdentity(ctx, sid); err != nil || ok {
		t.Fatalf("吊销后 HasActiveIdentity = %v, %v", ok, err)
	}
}

func TestIdentityGuardPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)
	b := identRenew(t, ctx, e, sid)
	serialA, serialB := a.Text(16), b.Text(16)

	bad := []struct {
		what string
		q    string
		args []any
	}{
		{"改证书", `UPDATE server_identities SET cert_pem = cert_pem || ' ' WHERE cert_serial = ?`, []any{serialA}},
		{"改指纹", `UPDATE server_identities SET spki_sha256 = repeat('0', 64) WHERE cert_serial = ?`, []any{serialA}},
		{"删除", `DELETE FROM server_identities WHERE cert_serial = ?`, []any{serialA}},
		{"active 退回 pending", `UPDATE server_identities SET status = 'pending' WHERE cert_serial = ?`, []any{serialA}},
		{"两份 pending", `INSERT INTO server_identities (server_id, seq, cert_serial, spki_sha256, cert_pem, source, status, issued_at, expires_at)
			SELECT server_id, 99, 'abc', spki_sha256, cert_pem, 'renew', 'pending', issued_at, expires_at
			FROM server_identities WHERE cert_serial = ?`, []any{serialB}},
	}
	for _, c := range bad {
		if err := identSavepoint(t, ctx, e, c.q, c.args...); err == nil {
			t.Fatalf("%s 应被库拒绝", c.what)
		}
	}

	// 终态冻结：revoked 不能再改
	if err := e.exec(ctx, `UPDATE server_identities SET status = 'revoked', revoked_at = now(), revoked_reason = 'x'
		WHERE cert_serial = ?`, serialB); err != nil {
		t.Fatal(err)
	}
	if err := identSavepoint(t, ctx, e, `UPDATE server_identities SET status = 'active', revoked_at = NULL, revoked_reason = NULL
		WHERE cert_serial = ?`, serialB); err == nil {
		t.Fatal("revoked 不能回到 active")
	}
}

func TestLoadOrCreateCAPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	// 库里可能已有别的用例（或 serve）真提交的 CA：在本回滚事务里临时绕过守卫清掉，结束时随事务回滚
	for _, q := range []string{
		`SET LOCAL session_replication_role = replica`,
		`DELETE FROM node_ca`,
		`SET LOCAL session_replication_role = origin`,
	} {
		if err := e.exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sealer, err := e.keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.svc.LoadOrCreateCA(ctx, sealer)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.LoadOrCreateCA(ctx, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != again.Fingerprint() || string(first.CertPEM()) != string(again.CertPEM()) {
		t.Fatal("第二次应加载同一个 CA")
	}
	var spki string
	var sealed []byte
	if err := e.row(ctx, `SELECT spki_sha256, key_sealed FROM node_ca`).Scan(&spki, &sealed); err != nil {
		t.Fatal(err)
	}
	if "sha256:"+spki != first.Fingerprint() {
		t.Fatalf("库里指纹 %s 与 CA %s 不一致", spki, first.Fingerprint())
	}
	sum := sha256.Sum256(first.Certificate().RawSubjectPublicKeyInfo)
	if spki != hex.EncodeToString(sum[:]) {
		t.Fatal("指纹不是 SPKI 的 SHA-256")
	}
	if len(sealed) == 0 {
		t.Fatal("私钥应封存存库")
	}
	if err := identSavepoint(t, ctx, e, `UPDATE node_ca SET spki_sha256 = spki_sha256`); err == nil {
		t.Fatal("node_ca 不许改")
	}

	// 换了主密钥打不开
	other, err := identMasterKeys(t, 0x02).Sealer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoadOrCreateCA(ctx, other); err == nil {
		t.Fatal("主密钥不对应报错")
	}
}
