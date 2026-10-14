package service

// 节点身份加固的 PG 测试：续期绑定建流证书、续期限频、吊销作废令牌、LatestIdentity 只看转正过的、
// 退役服务器拒绝、令牌与 activated_at 的库守卫。环境与夹具在 identity_pg_test.go。

import (
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/reqid"
)

func TestRenewBoundToStreamCertPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)
	a := identEnroll(t, ctx, e, sid)
	e.advance(renewWindow)
	b := identRenew(t, ctx, e, sid, a)
	if err := e.svc.AuthorizeSession(ctx, sid, b); err != nil {
		t.Fatalf("pending 建流应转正：%v", err)
	}
	if got := identStatus(t, ctx, e, a); got != "superseded" {
		t.Fatalf("a 应被取代，得到 %s", got)
	}

	// 拿着已被取代的旧证书建的流不能再换证书
	e.advance(2 * time.Minute)
	_, _, err := e.svc.RenewCertificate(ctx, sid, a, identCSR(t, identKey(t)))
	wantSessionRevoked(t, err, "旧证书续期")
	// 当前 active 的 b 可以续（b 刚签出，剩余 30 天：拨到它的续期窗口）
	e.advance(renewWindow)
	identRenew(t, ctx, e, sid, b)

	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Minute)
	_, _, err = e.svc.RenewCertificate(ctx, sid, b, identCSR(t, identKey(t)))
	wantSessionRevoked(t, err, "吊销后续期")
}

func TestRenewRateLimitPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)

	_, _, err := e.svc.RenewCertificate(ctx, sid, a, identCSR(t, identKey(t)))
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("剩余有效期还长时续期应回普通错误，得到 %v", err)
	}
	e.advance(renewWindow)
	b := identRenew(t, ctx, e, sid, a)

	// 不拨时钟立刻再续：1 分钟内，普通错误
	_, _, err = e.svc.RenewCertificate(ctx, sid, a, identCSR(t, identKey(t)))
	if err == nil || errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("1 分钟内再续应回普通错误，得到 %v", err)
	}
	if got := identStatus(t, ctx, e, b); got != "pending" {
		t.Fatalf("被拒的续期不应动已有 pending，得到 %s", got)
	}
	c := identRenew(t, ctx, e, sid, a) // 内部先拨 2 分钟
	if got := identStatus(t, ctx, e, b); got != "superseded" {
		t.Fatalf("b 应被取代，得到 %s", got)
	}
	if got := identStatus(t, ctx, e, c); got != "pending" {
		t.Fatalf("c 应 pending，得到 %s", got)
	}
}

func TestRevokeVoidsUnusedTokensPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)
	identEnroll(t, ctx, e, sid)
	t1 := identIssueToken(t, e, sid)
	t2 := identIssueToken(t, e, sid)

	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); err != nil {
		t.Fatal(err)
	}
	var voided string
	if err := e.row(ctx, `SELECT coalesce(data->>'voided_tokens', '') FROM audit_events
		WHERE action = 'node.identity.revoke' AND resource_id = ?`, sid).Scan(&voided); err != nil {
		t.Fatal(err)
	}
	if voided != "2" {
		t.Fatalf("吊销审计 voided_tokens 应为 2，得到 %q", voided)
	}
	view, err := e.svc.NodeIdentity(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if view.PendingTokens != 0 {
		t.Fatalf("作废后待用令牌应为 0，得到 %d", view.PendingTokens)
	}
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: t1, CSRDER: identCSR(t, identKey(t)), RequestID: reqid.New()}); !errors.Is(err, ErrEnrollDenied) {
		t.Fatalf("作废的令牌接入应拒绝，得到 %v", err)
	}
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM bootstrap_tokens
		WHERE server_id = ? AND voided_at IS NOT NULL AND used_at IS NULL
		  AND token_sha256 IN (encode(sha256(convert_to(?, 'UTF8')), 'hex'), encode(sha256(convert_to(?, 'UTF8')), 'hex'))`,
		sid, t1, t2).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("两张令牌都应 voided_at 非空，得到 %d", n)
	}
}

func TestLatestIdentityIgnoresNeverActivatedPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	nodeID := seedIdentNode(t, ctx, e, sid)
	a := identEnroll(t, ctx, e, sid)
	e.advance(renewWindow)
	b := identRenew(t, ctx, e, sid, a)
	c := identRenew(t, ctx, e, sid, a) // b 被取代，从没转正

	want := func(step string, seq int32, status string) {
		t.Helper()
		view, err := e.svc.NodeIdentity(ctx, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if view.Identity == nil || view.Identity.Seq != seq || view.Identity.Status != status {
			t.Fatalf("%s：应展示 seq %d %s，得到 %+v", step, seq, status, view.Identity)
		}
	}
	want("续期两次后", 1, "active")

	if err := e.svc.RevokeNodeIdentity(e.admin, nodeID); err != nil {
		t.Fatal(err)
	}
	// c 被吊销但从没转正、b 被取代也从没转正：都不展示
	want("吊销后", 1, "revoked")

	for _, w := range []struct {
		serial *big.Int
		set    bool
	}{{a, true}, {b, false}, {c, false}} {
		var set bool
		if err := e.row(ctx, `SELECT activated_at IS NOT NULL FROM server_identities WHERE cert_serial = ?`,
			w.serial.Text(16)).Scan(&set); err != nil {
			t.Fatal(err)
		}
		if set != w.set {
			t.Fatalf("%s 的 activated_at 非空 = %v，期望 %v", w.serial.Text(16), set, w.set)
		}
	}
}

func TestAuthorizeSessionRejectsRetiredPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now() WHERE id = ?`, sid); err != nil {
		t.Fatal(err)
	}
	wantSessionRevoked(t, e.svc.AuthorizeSession(ctx, sid, a), "退役服务器建流")
	e.advance(renewWindow)
	_, _, err := e.svc.RenewCertificate(ctx, sid, a, identCSR(t, identKey(t)))
	wantSessionRevoked(t, err, "退役服务器续期")
}

func TestBootstrapTokenGuardPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	other := seedIdentServer(t, ctx, e, "ready")
	t1 := identIssueToken(t, e, sid)
	t2 := identIssueToken(t, e, sid)
	if _, err := e.svc.Enroll(ctx, EnrollInput{Token: t1, CSRDER: identCSR(t, identKey(t)), RequestID: reqid.New()}); err != nil {
		t.Fatal(err)
	}
	const byToken = `token_sha256 = encode(sha256(convert_to(?, 'UTF8')), 'hex')`

	bad := []struct {
		what string
		q    string
		args []any
	}{
		{"改过期时间", `UPDATE bootstrap_tokens SET expires_at = expires_at + interval '1 minute' WHERE ` + byToken, []any{t2}},
		{"改哈希", `UPDATE bootstrap_tokens SET token_sha256 = repeat('0', 64) WHERE ` + byToken, []any{t2}},
		{"改服务器", `UPDATE bootstrap_tokens SET server_id = ? WHERE ` + byToken, []any{other, t2}},
		{"删除", `DELETE FROM bootstrap_tokens WHERE ` + byToken, []any{t2}},
		{"改已用时间", `UPDATE bootstrap_tokens SET used_at = used_at + interval '1 second' WHERE ` + byToken, []any{t1}},
		{"已用退回未用", `UPDATE bootstrap_tokens SET used_at = NULL, used_request_id = NULL WHERE ` + byToken, []any{t1}},
		{"已用的再作废", `UPDATE bootstrap_tokens SET voided_at = now() WHERE ` + byToken, []any{t1}},
	}
	for _, c := range bad {
		if err := identSavepoint(t, ctx, e, c.q, c.args...); err == nil {
			t.Fatalf("%s 应被库拒绝", c.what)
		}
	}

	if err := identSavepoint(t, ctx, e, `UPDATE bootstrap_tokens SET voided_at = now() WHERE `+byToken, t2); err != nil {
		t.Fatalf("未用令牌作废应允许：%v", err)
	}
	if err := identSavepoint(t, ctx, e, `UPDATE bootstrap_tokens SET voided_at = voided_at + interval '1 second' WHERE `+byToken, t2); err == nil {
		t.Fatal("voided_at 只能写一次")
	}
	if err := identSavepoint(t, ctx, e, `UPDATE bootstrap_tokens SET issued_by = NULL WHERE `+byToken, t2); err != nil {
		t.Fatalf("issued_by 置空应允许（用户删除时 SET NULL）：%v", err)
	}
}

func TestIdentityActivatedAtGuardPG(t *testing.T) {
	e, ctx := newIdentEnv(t, false)
	sid := seedIdentServer(t, ctx, e, "ready")
	a := identEnroll(t, ctx, e, sid)
	for _, q := range []string{
		`UPDATE server_identities SET activated_at = now() WHERE cert_serial = ?`,
		`UPDATE server_identities SET activated_at = NULL WHERE cert_serial = ?`,
	} {
		if err := identSavepoint(t, ctx, e, q, a.Text(16)); err == nil {
			t.Fatalf("应被库拒绝：%s", q)
		}
	}
}
