package service

// 安装令牌与接入（Enroll）的 PG 测试；环境与夹具在 identity_pg_test.go。

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	idemrepo "github.com/aegispanel/aegis/internal/module/idempotency/repo"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// nodeFakeIdem 记下以「不可重放」完成的幂等认领。
type nodeFakeIdem struct {
	unreplayable []*idemsvc.Claim
	status       []int
	err          error
}

func (f *nodeFakeIdem) CompleteUnreplayable(_ context.Context, c *idemsvc.Claim, status int) error {
	if f.err != nil {
		return f.err
	}
	f.unreplayable = append(f.unreplayable, c)
	f.status = append(f.status, status)
	return nil
}

func bootstrapTokenCount(t *testing.T, ctx context.Context, e *identEnv, sid uuid.UUID) int64 {
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM bootstrap_tokens WHERE server_id = ?`, sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 带幂等认领时在签发事务里以不可重放完成（状态 201）；未装配或完成失败时整个签发回滚。
// 用真提交环境：回滚环境里 tx.Do 加入外层测试事务，签发失败不会回滚已插的令牌行，验不出 c、d。
func TestIssueBootstrapTokenUnreplayablePG(t *testing.T) {
	e, ctx := newIdentEnv(t, true)
	sid := seedIdentServer(t, ctx, e, "ready")
	claim := &idemsvc.Claim{Scope: "server_bootstrap_token", Key: "k1"}

	// a：带 claim 且已装配 fake，事务内完成不可重放认领，状态码 201
	fake := &nodeFakeIdem{}
	e.svc.UseIdempotency(fake)
	ctxClaim := idemsvc.WithClaim(e.admin, claim)
	if _, err := e.svc.IssueBootstrapToken(ctxClaim, sid, 0); err != nil {
		t.Fatal(err)
	}
	if len(fake.unreplayable) != 1 {
		t.Fatalf("应 CompleteUnreplayable 1 次，得到 %d", len(fake.unreplayable))
	}
	if fake.unreplayable[0] != claim {
		t.Fatal("认领指针应与 WithClaim 传入的一致")
	}
	if fake.status[0] != http.StatusCreated {
		t.Fatalf("状态码应为 201，得到 %d", fake.status[0])
	}

	// b：不带 claim，fake 次数不再增加
	before := len(fake.unreplayable)
	if _, err := e.svc.IssueBootstrapToken(e.admin, sid, 0); err != nil {
		t.Fatal(err)
	}
	if len(fake.unreplayable) != before {
		t.Fatalf("无 claim 时不应再调 CompleteUnreplayable，得到 %d 次", len(fake.unreplayable))
	}

	// c：带 claim 但未装配（nil），Internal 且令牌行数不增
	e.svc.UseIdempotency(nil)
	nBefore := bootstrapTokenCount(t, ctx, e, sid)
	_, err := e.svc.IssueBootstrapToken(idemsvc.WithClaim(e.admin, &idemsvc.Claim{Scope: "server_bootstrap_token", Key: "k2"}), sid, 0)
	if poolErrCode(err) != errs.Internal {
		t.Fatalf("未装配应 Internal，得到 %v", err)
	}
	if bootstrapTokenCount(t, ctx, e, sid) != nBefore {
		t.Fatal("未装配时事务应回滚，bootstrap_tokens 行数不变")
	}

	// d：fake 返回错误，带 claim 签发失败且行数不增
	e.svc.UseIdempotency(&nodeFakeIdem{err: errors.New("idem fail")})
	nBefore = bootstrapTokenCount(t, ctx, e, sid)
	_, err = e.svc.IssueBootstrapToken(idemsvc.WithClaim(e.admin, &idemsvc.Claim{Scope: "server_bootstrap_token", Key: "k3"}), sid, 0)
	if err == nil {
		t.Fatal("fake 报错时应失败")
	}
	if bootstrapTokenCount(t, ctx, e, sid) != nBefore {
		t.Fatal("CompleteUnreplayable 失败时事务应回滚，bootstrap_tokens 行数不变")
	}
}

// 真实 idempotency.Service 下签发安装令牌：钉住「不可重放完成、令牌明文不进 idempotency_keys、重放回 409 且不泄露令牌」。
func TestIssueBootstrapTokenRealIdempotencyPG(t *testing.T) {
	e, ctx := newIdentEnv(t, true)
	sid := seedIdentServer(t, ctx, e, "ready")

	d := testkit.DB(t)
	idem := idemsvc.New(idemrepo.New(d), e.txm)
	e.svc.UseIdempotency(idem)

	fp := idemsvc.NewFingerprint([]byte("测试指纹密钥-0123456789abcdef"), "POST", "/v1/servers/"+sid.String()+"/bootstrap-token", []byte(`{"ttl_minutes":20}`))
	key := "boot-" + uuid.NewString()

	t.Cleanup(func() {
		d.Conn(context.Background()).Exec("DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?", "server_bootstrap_token", key)
	})

	claim, replay, err := idem.Claim(e.admin, "server_bootstrap_token", key, fp)
	if err != nil {
		t.Fatalf("Claim 失败：%v", err)
	}
	if replay != nil {
		t.Fatalf("首回认领 replay 应为 nil，得到 %+v", replay)
	}
	if claim == nil {
		t.Fatal("首回认领 claim 不应为 nil")
	}

	out, err := e.svc.IssueBootstrapToken(idemsvc.WithClaim(e.admin, claim), sid, 20)
	if err != nil {
		t.Fatalf("签发安装令牌失败：%v", err)
	}
	if out.Token == "" {
		t.Fatal("签发成功但令牌为空")
	}

	var (
		status       string
		responseCode int
		replayable   bool
		bodyIsNull   bool
	)
	if err := e.row(ctx, `SELECT status, response_code, replayable, response_body IS NULL FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`,
		"server_bootstrap_token", key).Scan(&status, &responseCode, &replayable, &bodyIsNull); err != nil {
		t.Fatalf("查询 idempotency_keys 记录失败：%v", err)
	}
	if status != "succeeded" {
		t.Fatalf("幂等记录状态应为 succeeded，得到 %s", status)
	}
	if responseCode != 201 {
		t.Fatalf("幂等记录响应码应为 201，得到 %d", responseCode)
	}
	if replayable {
		t.Fatal("幂等记录 replayable 应为 false")
	}
	if !bodyIsNull {
		t.Fatal("幂等记录 response_body 应为 NULL")
	}

	var count int64
	if err := e.row(ctx, `SELECT count(*) FROM idempotency_keys WHERE scope = ? AND idempotency_key = ? AND position(convert_to(?, 'UTF8') in coalesce(response_body, ''::bytea)) > 0`,
		"server_bootstrap_token", key, out.Token).Scan(&count); err != nil {
		t.Fatalf("校验令牌明文未进库失败：%v", err)
	}
	if count != 0 {
		t.Fatalf("令牌明文不应落进幂等表 response_body，匹配到 %d 行", count)
	}

	_, replay2, err := idem.Claim(e.admin, "server_bootstrap_token", key, fp)
	if err == nil {
		t.Fatal("同键重放不可重放的认领应报错")
	}
	if code := errs.As(err).Code; code != errs.Conflict {
		t.Fatalf("同键重放错误码应为 conflict，得到 %s（err=%v）", code, err)
	}
	if replay2 != nil {
		t.Fatalf("不可重放的记录 replay 应为 nil，得到 %+v", replay2)
	}
	if strings.Contains(err.Error(), out.Token) {
		t.Fatalf("重放 409 报错文案不应包含令牌明文：%v", err)
	}
}

func TestIssueBootstrapTokenPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)

	out, err := e.svc.IssueBootstrapToken(e.admin, sid, 30)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"'https://panel.example.test/pdnd/install.sh'",
		"--panel 'node.example.test:9443'",
		"--ca-sha256 '" + e.ca.Fingerprint() + "'",
		"--token '" + out.Token + "'",
	} {
		if !strings.Contains(out.InstallCommand, want) {
			t.Fatalf("安装命令缺 %q：%s", want, out.InstallCommand)
		}
	}
	// 库里只有哈希
	sum := sha256.Sum256([]byte(out.Token))
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM bootstrap_tokens WHERE server_id = ? AND token_sha256 = ? AND issued_by IS NOT NULL`,
		sid, hex.EncodeToString(sum[:])).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应存一行令牌哈希，得到 %d", n)
	}
	if err := e.row(ctx, `SELECT count(*) FROM bootstrap_tokens WHERE token_sha256 = ?`, out.Token).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不应存明文：n=%d err=%v", n, err)
	}
	if c := identAuditCount(t, ctx, e, "node.bootstrap_token.issue", sid); c != 1 {
		t.Fatalf("签发审计应 1 条，得到 %d", c)
	}
	view, err := e.svc.NodeIdentity(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if view.PendingTokens != 1 || view.Identity != nil {
		t.Fatalf("应有 1 张待用令牌、还没有身份：%+v", view)
	}

	if _, err := e.svc.IssueBootstrapToken(e.admin, sid, 61); poolErrCode(err) != errs.ValidationFailed {
		t.Fatalf("ttl 61 应 422，得到 %v", err)
	}
	if _, err := e.svc.IssueBootstrapToken(e.admin, uuid.New(), 0); poolErrCode(err) != errs.NotFound {
		t.Fatalf("服务器不存在应 404，得到 %v", err)
	}
	retired := seedIdentServer(t, ctx, e, "retired")
	if _, err := e.svc.IssueBootstrapToken(e.admin, retired, 0); poolErrCode(err) != errs.Conflict {
		t.Fatalf("已退役的服务器应 409，得到 %v", err)
	}
}

func TestEnrollPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	token := identIssueToken(t, e, sid)
	res, err := e.svc.Enroll(ctx, EnrollInput{
		Token: token, CSRDER: identCSR(t, identKey(t)), RequestID: reqid.New(), AgentVersion: "pdnd/1.0", RemoteIP: "192.0.2.10",
		Host: EnrollHostInfo{Hostname: "host.example.test", OS: "linux", Arch: "amd64", CPUCores: 2, MemoryBytes: 2 << 30, DiskBytes: 40 << 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ServerID != sid || string(res.CAPEM) != string(e.ca.CertPEM()) {
		t.Fatalf("结果不对：%v", res.ServerID)
	}
	leaf := identLeaf(t, res.ChainPEM)
	roots := x509.NewCertPool()
	roots.AddCert(e.ca.Certificate())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("客户端证书应由面板 CA 签发：%v", err)
	}
	if id, ok := ServerIDFromCert(leaf); !ok || id != sid {
		t.Fatalf("证书里的服务器 id = %v, %v", id, ok)
	}
	if !strings.Contains(string(res.ChainPEM), strings.TrimSpace(string(e.ca.CertPEM()))) {
		t.Fatal("证书链应带 CA")
	}
	if err := e.svc.AuthorizeSession(ctx, sid, leaf.SerialNumber); err != nil {
		t.Fatalf("新证书应能建流：%v", err)
	}
	if got := identStatus(t, ctx, e, leaf.SerialNumber); got != "active" {
		t.Fatalf("接入签出的证书直接 active，得到 %s", got)
	}

	var hostname, osName, agent string
	var cpu, mem, disk int32
	if err := e.row(ctx, `SELECT hostname, os_name, agent_version, cpu_cores, memory_mb, disk_gb FROM servers WHERE id = ?`, sid).
		Scan(&hostname, &osName, &agent, &cpu, &mem, &disk); err != nil {
		t.Fatal(err)
	}
	if hostname != "host.example.test" || osName != "linux" || agent != "pdnd/1.0" || cpu != 2 || mem != 2048 || disk != 40 {
		t.Fatalf("主机信息 = %s %s %s %d %d %d", hostname, osName, agent, cpu, mem, disk)
	}
	if v4, v6 := identServerIPs(t, ctx, e, sid); v4 != "192.0.2.10" || v6 != "" {
		t.Fatalf("接入应按来源地址回填公网 IPv4，得到 %q / %q", v4, v6)
	}
	var actor, remoteIP string
	if err := e.row(ctx, `SELECT actor_kind, coalesce(data->>'remote_ip', '') FROM audit_events
		WHERE action = 'server.enrollment.commit' AND resource_id = ?`, sid).Scan(&actor, &remoteIP); err != nil {
		t.Fatal(err)
	}
	if actor != "system" {
		t.Fatalf("接入审计的操作者应为 system，得到 %s", actor)
	}
	if remoteIP != "192.0.2.10" {
		t.Fatalf("接入审计应记来源 IP，得到 %q", remoteIP)
	}
}

// 管理员填过的 IP 不覆盖；内网来源不回填；IPv6 来源只填 public_ipv6。
func TestEnrollPublicIPFillPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	enroll := func(sid uuid.UUID, remote string) {
		t.Helper()
		if _, err := e.svc.Enroll(ctx, EnrollInput{
			Token: identIssueToken(t, e, sid), CSRDER: identCSR(t, identKey(t)), RequestID: reqid.New(), RemoteIP: remote,
		}); err != nil {
			t.Fatal(err)
		}
	}

	kept := seedIdentServer(t, ctx, e, "draft")
	if err := e.exec(ctx, `UPDATE servers SET public_ipv4 = '198.51.100.7' WHERE id = ?`, kept); err != nil {
		t.Fatal(err)
	}
	enroll(kept, "192.0.2.10")
	if v4, _ := identServerIPs(t, ctx, e, kept); v4 != "198.51.100.7" {
		t.Fatalf("管理员填过的 IPv4 不该被覆盖，得到 %q", v4)
	}
	// 已有 IPv4 的机器从 IPv6 重新接入：只补空着的 IPv6
	enroll(kept, "2001:db8::2")
	if v4, v6 := identServerIPs(t, ctx, e, kept); v4 != "198.51.100.7" || v6 != "2001:db8::2" {
		t.Fatalf("应保留 IPv4、补上 IPv6，得到 %q / %q", v4, v6)
	}

	private := seedIdentServer(t, ctx, e, "draft")
	enroll(private, "10.0.0.5")
	if v4, v6 := identServerIPs(t, ctx, e, private); v4 != "" || v6 != "" {
		t.Fatalf("内网来源不该回填，得到 %q / %q", v4, v6)
	}

	v6only := seedIdentServer(t, ctx, e, "draft")
	enroll(v6only, "2001:db8::1")
	if v4, v6 := identServerIPs(t, ctx, e, v6only); v4 != "" || v6 != "2001:db8::1" {
		t.Fatalf("IPv6 来源应只填 public_ipv6，得到 %q / %q", v4, v6)
	}
}

// identServerIPs 读服务器的公网 IPv4 / IPv6（空列读成空串）。
func identServerIPs(t *testing.T, ctx context.Context, e *identEnv, sid uuid.UUID) (string, string) {
	t.Helper()
	var v4, v6 string
	if err := e.row(ctx, `SELECT coalesce(host(public_ipv4), ''), coalesce(host(public_ipv6), '') FROM servers WHERE id = ?`, sid).
		Scan(&v4, &v6); err != nil {
		t.Fatal(err)
	}
	return v4, v6
}

func TestEnrollDeniedPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	token := identIssueToken(t, e, sid)
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: token, CSRDER: identCSR(t, identKey(t)), RequestID: "req-1"}); err != nil {
		t.Fatal(err)
	}

	// 已用（别的 request_id）、不存在
	for _, in := range []EnrollInput{
		{Token: token, CSRDER: identCSR(t, identKey(t)), RequestID: "req-2"},
		{Token: "no-such-token", CSRDER: identCSR(t, identKey(t)), RequestID: "req-3"},
	} {
		if _, err := e.svc.Enroll(ctx, in); !errors.Is(err, ErrEnrollDenied) {
			t.Fatalf("应拒绝，得到 %v", err)
		}
	}

	// 过期
	expired := "expired-token-example"
	sum := sha256.Sum256([]byte(expired))
	if err := e.exec(ctx, `INSERT INTO bootstrap_tokens (server_id, token_sha256, created_at, expires_at)
		VALUES (?, ?, now() - interval '30 minutes', now() - interval '10 minutes')`, sid, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: expired, CSRDER: identCSR(t, identKey(t)), RequestID: "req-4"}); !errors.Is(err, ErrEnrollDenied) {
		t.Fatalf("过期令牌应拒绝，得到 %v", err)
	}

	// 签令牌之后服务器退役
	sid2 := seedIdentServer(t, ctx, e, "ready")
	token2 := identIssueToken(t, e, sid2)
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now() WHERE id = ?`, sid2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: token2, CSRDER: identCSR(t, identKey(t)), RequestID: "req-5"}); !errors.Is(err, ErrEnrollDenied) {
		t.Fatalf("已退役的服务器应拒绝，得到 %v", err)
	}

	// 请求本身不合法
	for _, in := range []EnrollInput{
		{Token: "", CSRDER: identCSR(t, identKey(t)), RequestID: "req-6"},
		{Token: "t", CSRDER: identCSR(t, identKey(t)), RequestID: "bad id"},
		{Token: "t", CSRDER: []byte("bad"), RequestID: "req-7"},
	} {
		if _, err := e.svc.Enroll(ctx, in); !errors.Is(err, ErrEnrollInvalid) {
			t.Fatalf("应回 ErrEnrollInvalid，得到 %v", err)
		}
	}
}

func TestEnrollRetrySameRequestPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	token := identIssueToken(t, e, sid)
	key := identKey(t)
	first, err := e.svc.Enroll(ctx, EnrollInput{Token: token, CSRDER: identCSR(t, key), RequestID: "req-retry"})
	if err != nil {
		t.Fatal(err)
	}
	// 节点崩溃后用同一把钥匙重签 CSR（字节不同）重试：同一张证书
	again, err := e.svc.Enroll(ctx, EnrollInput{Token: token, CSRDER: identCSR(t, key), RequestID: "req-retry"})
	if err != nil {
		t.Fatalf("同一 request_id 同一公钥重试应成功：%v", err)
	}
	if string(again.ChainPEM) != string(first.ChainPEM) || again.ServerID != sid {
		t.Fatal("重试应返回同一张证书")
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM server_identities WHERE server_id = ?`, sid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("重试不应再签：n=%d err=%v", n, err)
	}
	// 同一 request_id 换了公钥：拒绝
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: token, CSRDER: identCSR(t, identKey(t)), RequestID: "req-retry"}); !errors.Is(err, ErrEnrollDenied) {
		t.Fatalf("换了公钥应拒绝，得到 %v", err)
	}
}

func TestEnrollReenrollSupersedesPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)
	e.advance(renewWindow)
	b := identRenew(t, ctx, e, sid, a)
	c := identEnroll(t, ctx, e, sid)
	for _, s := range []*big.Int{a, b} {
		if got := identStatus(t, ctx, e, s); got != "superseded" {
			t.Fatalf("重新接入后旧的 active 与 pending 都应 superseded，%s 得到 %s", s.Text(16), got)
		}
	}
	if got := identStatus(t, ctx, e, c); got != "active" {
		t.Fatalf("新接入的应 active，得到 %s", got)
	}
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, a), "重新接入后的旧证书")
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, b), "重新接入后的 pending")
	if err := e.svc.AuthorizeSession(ctx, sid, c); err != nil {
		t.Fatal(err)
	}
}

// 真提交：同一张令牌并发接入只成功一次；重新接入在提交后通知会话层断掉旧身份的流。
func TestEnrollConcurrentSameTokenPG(t *testing.T) {
	e, ctx := newIdentEnv(t, true)
	sid := seedIdentServer(t, ctx, e, "ready")
	token := identIssueToken(t, e, sid)

	const n = 4
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		csr := identCSR(t, identKey(t))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = e.svc.Enroll(ctx, EnrollInput{Token: token, CSRDER: csr, RequestID: "req-" + uuid.NewString()[:8]})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrEnrollDenied):
			t.Fatalf("并发失败的一方应是 ErrEnrollDenied，得到 %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("同一张令牌应只成功一次，成功 %d 次", ok)
	}
	if got := e.tombs.list(); len(got) != 0 {
		t.Fatalf("第一次接入没有旧身份，不应 Tombstone：%v", got)
	}

	identEnroll(t, ctx, e, sid)
	want := sid.String() + ":" + TombstoneIdentitySuperseded
	if got := e.tombs.list(); len(got) != 1 || got[0] != want {
		t.Fatalf("重新接入提交后应 Tombstone %s，得到 %v", want, got)
	}
}
