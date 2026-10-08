package certs

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// PG18 门禁（run-pg18-gates.sh 的 certs 域，先跑 configure-app-role.sql 再以 aegis_app 测）：
// 租户隔离、证书版本不可改、两个 worker 抢同一张订单只有一个成功、租约过期接管、凭据与 EAB 的
// PATCH 缺字段保留原值、读形状里没有密文；以及 worker 接 pebble + 模拟 Cloudflare 的整条链路：
// 签发、续期带 ARI replaces、CA 429 退避、令牌失效时不碰 CA、预计超限时本地拦截并切备用 CA。
//
// 夹具租户 id 用 ce57 前缀（与其他域不撞号），每个子测试一个租户。

var certsFixture = pg18test.Fixture{
	Domain:         "CERTS",
	DatabasePrefix: "pandora_certs_",
	MarkerTable:    "pandora_certs_test_marker",
	CommentTag:     "pandora-certs-pg18",
}

type pgEnv struct {
	*issueEnv
	ctx    context.Context
	admin  *pgxpool.Pool
	tenant string
	actor  Actor
}

func newPGEnv(t *testing.T, admin *pgxpool.Pool, app *platformdb.Pool, n int) *pgEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	tenant := fmt.Sprintf("ce570000-0000-4000-8000-%012d", n)
	user := fmt.Sprintf("ce570000-0000-4000-8000-%012d", n+500)
	for _, q := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenant + `','certs-pg18-` + fmt.Sprint(n) + `','Certs','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + user + `','` + tenant + `','ops` + fmt.Sprint(n) + `@certs.invalid','Ops','active')`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	e := newIssueEnv(t, Options{})
	e.svc.pool = app
	return &pgEnv{issueEnv: e, ctx: ctx, admin: admin, tenant: tenant, actor: Actor{Kind: "admin", ID: user}}
}

func (e *pgEnv) credential(t *testing.T, name, token string) VerifyResult {
	t.Helper()
	res, err := e.svc.CreateDNSCredential(e.ctx, e.tenant, e.actor, DNSCredentialInput{Name: name,
		Provider: ProviderCloudflare, Zone: testZone, Secret: map[string]string{"api_token": token}})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return res
}

func (e *pgEnv) certificate(t *testing.T, name, credID string, ids ...string) Certificate {
	t.Helper()
	c, err := e.svc.CreateCertificate(e.ctx, e.tenant, e.actor, CertificateInput{Name: name, Identifiers: ids,
		DNSCredentialID: credID})
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return c
}

func (e *pgEnv) worker() *Worker {
	w := e.svc.NewWorker(slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.lastSweep = time.Now() // ARI 与到期等级的刷新单独测，这里每轮只排单、签单
	return w
}

func (e *pgEnv) run(t *testing.T) {
	t.Helper()
	if _, err := e.worker().RunOnce(e.ctx, e.tenant); err != nil {
		t.Fatalf("worker run: %v", err)
	}
}

func (e *pgEnv) scalar(t *testing.T, q string, args ...any) any {
	t.Helper()
	var v any
	if err := e.admin.QueryRow(e.ctx, q, args...).Scan(&v); err != nil {
		t.Fatalf("query %s: %v", q, err)
	}
	return v
}

func (e *pgEnv) order(t *testing.T, certID string) (state, reason string, code, replaces *string) {
	t.Helper()
	if err := e.admin.QueryRow(e.ctx, `SELECT state, reason, error_code, replaces_ari_id FROM certificate_orders
		 WHERE certificate_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, certID).Scan(&state, &reason, &code, &replaces); err != nil {
		t.Fatalf("latest order: %v", err)
	}
	return
}

func TestCertsPG18(t *testing.T) {
	_, admin, app := pg18test.Open(t, certsFixture)

	t.Run("tenant isolation and immutable versions", func(t *testing.T) {
		a := newPGEnv(t, admin, app, 1)
		b := newPGEnv(t, admin, app, 2)
		cred := a.credential(t, "cf", cfToken)
		cert := a.certificate(t, "wild", cred.Credential.ID, "*.example.com")
		if list, err := b.svc.ListCertificates(b.ctx, b.tenant); err != nil || len(list.Items) != 0 {
			t.Fatalf("tenant B sees %d certificates err=%v", len(list.Items), err)
		}
		if creds, err := b.svc.ListDNSCredentials(b.ctx, b.tenant); err != nil || len(creds) != 0 {
			t.Fatalf("tenant B sees %d credentials err=%v", len(creds), err)
		}
		var he *httpx.Error
		if _, err := b.svc.CertificateDetail(b.ctx, b.tenant, cert.ID); !errors.As(err, &he) || he.Code != httpx.CodeNotFound {
			t.Fatalf("cross-tenant detail must be 404, got %v", err)
		}
		// RLS 本身：以 B 的租户上下文直查五张表，一行都看不到
		if err := app.InTx(b.ctx, platformdb.Scope{TenantID: b.tenant}, func(tx pgx.Tx) error {
			for _, table := range []string{"certificates", "certificate_orders", "certificate_versions", "dns_credentials", "acme_accounts"} {
				var n int
				if err := tx.QueryRow(b.ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
					return fmt.Errorf("%s visible to tenant B: n=%d err=%v", table, n, err)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// 签出一张，再证明版本行改不了：授权上没有 UPDATE，触发器也挡
		a.run(t)
		if v := a.scalar(t, `SELECT count(*) FROM certificate_versions WHERE certificate_id = $1`, cert.ID); v.(int64) != 1 {
			t.Fatalf("versions = %v", v)
		}
		for priv, want := range map[string]bool{"UPDATE": false, "INSERT": true, "SELECT": true, "DELETE": true, "TRUNCATE": false} {
			if got := a.scalar(t, `SELECT has_table_privilege('aegis_app', 'public.certificate_versions', $1)`, priv); got.(bool) != want {
				t.Errorf("aegis_app %s on certificate_versions = %v, want %v", priv, got, want)
			}
		}
		err := app.InTx(a.ctx, platformdb.Scope{TenantID: a.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(a.ctx, `UPDATE certificate_versions SET serial = 'ff' WHERE certificate_id = $1`, cert.ID)
			return err
		})
		if !platformdb.IsInsufficientPrivilege(err) {
			t.Fatalf("aegis_app UPDATE on certificate_versions must be denied, got %v", err)
		}
		if _, err := admin.Exec(a.ctx, `UPDATE certificate_versions SET serial = 'ff' WHERE certificate_id = $1`, cert.ID); err == nil ||
			!strings.Contains(err.Error(), "追加写") {
			t.Fatalf("even the owner hits the immutability trigger, got %v", err)
		}
	})

	t.Run("secrets are write-only and kept when absent", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 3)
		created := e.credential(t, "cf", cfToken)
		if !created.OK || created.Credential.VerifyStatus != "ok" || created.Credential.SecretHint != cfToken[len(cfToken)-4:] {
			t.Fatalf("create+verify = %+v", created)
		}
		id := created.Credential.ID
		secretOf := func() map[string]string {
			var sealed []byte
			if err := e.admin.QueryRow(e.ctx, `SELECT secret_sealed FROM dns_credentials WHERE id = $1`, id).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			s, err := e.svc.openSecret(e.tenant, id, sealed)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		rename := "cf-renamed"
		if _, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, id, DNSCredentialPatch{Name: &rename}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, id, DNSCredentialPatch{Secret: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		if got := secretOf()["api_token"]; got != cfToken {
			t.Fatalf("PATCH without the token must keep it, got %q", got)
		}
		if _, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, id, DNSCredentialPatch{Secret: map[string]string{"api_token": ""}}); err == nil {
			t.Fatal("explicit empty token must be rejected")
		}
		res, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, id, DNSCredentialPatch{Secret: map[string]string{"api_token": "rotated-token-9999"}})
		if err != nil || res.OK || res.Credential.VerifyStatus != "error" || secretOf()["api_token"] != "rotated-token-9999" {
			t.Fatalf("rotated to a token the provider rejects: res=%+v err=%v", res, err)
		}
		// ACME 设置：EAB HMAC 缺席保留、显式空串清空
		hmac := "c2VjcmV0LWhtYWMta2V5"
		if _, err := e.svc.SaveACMESettings(e.ctx, e.tenant, e.actor, ACMESettingsInput{ContactEmail: "ops@example.com",
			ZeroSSLEnabled: true, ZeroSSLEABKID: "kid-1", ZeroSSLEABHMAC: &hmac}); err != nil {
			t.Fatal(err)
		}
		if s, err := e.svc.SaveACMESettings(e.ctx, e.tenant, e.actor, ACMESettingsInput{ContactEmail: "ops@example.com",
			ZeroSSLEnabled: true, ZeroSSLEABKID: "kid-1"}); err != nil || !s.ZeroSSLEABHMACSet {
			t.Fatalf("absent HMAC must be kept: %+v %v", s, err)
		}
		var cfg acmeConfig
		if err := app.InTx(e.ctx, platformdb.Scope{TenantID: e.tenant}, func(tx pgx.Tx) error {
			var err error
			cfg, err = e.svc.loadACMEConfig(e.ctx, tx, e.tenant, true)
			return err
		}); err != nil || cfg.eabHMAC != hmac {
			t.Fatalf("stored HMAC = %q err=%v", cfg.eabHMAC, err)
		}
		empty := ""
		if _, err := e.svc.SaveACMESettings(e.ctx, e.tenant, e.actor, ACMESettingsInput{ZeroSSLEnabled: true,
			ZeroSSLEABKID: "kid-1", ZeroSSLEABHMAC: &empty}); err == nil {
			t.Fatal("clearing the HMAC while ZeroSSL stays enabled must be rejected")
		}
		if s, err := e.svc.SaveACMESettings(e.ctx, e.tenant, e.actor, ACMESettingsInput{ZeroSSLEABHMAC: &empty}); err != nil || s.ZeroSSLEABHMACSet {
			t.Fatalf("explicit empty HMAC clears it: %+v %v", s, err)
		}
		// 读接口的 JSON：没有任何密文字段，也没有令牌、HMAC 的明文
		creds, _ := e.svc.ListDNSCredentials(e.ctx, e.tenant)
		settings, _ := e.svc.ACMESettings(e.ctx, e.tenant)
		for _, v := range []any{creds, settings, res} {
			raw, _ := json.Marshal(v)
			for _, banned := range []string{"sealed", cfToken, "rotated-token-9999", hmac, "api_token"} {
				if strings.Contains(string(raw), banned) {
					t.Fatalf("read JSON leaks %q: %s", banned, raw)
				}
			}
		}
	})

	t.Run("one active order and one winner per claim", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 4)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "one", cred.Credential.ID, "one.example.com")
		// 部分唯一索引：同一张证书第二张进行中的订单插不进去
		err := app.InTx(e.ctx, platformdb.Scope{TenantID: e.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(e.ctx, `INSERT INTO certificate_orders (tenant_id, certificate_id, reason) VALUES ($1, $2, 'manual')`,
				e.tenant, cert.ID)
			return err
		})
		if !platformdb.IsUniqueViolation(err) || platformdb.ConstraintName(err) != "certificate_orders_one_active" {
			t.Fatalf("second active order must hit the partial unique index, got %v", err)
		}
		// 并发「立即续期」：都回同一张订单
		var wg sync.WaitGroup
		ids := make([]string, 8)
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				o, err := e.svc.RenewCertificate(e.ctx, e.tenant, e.actor, cert.ID)
				if err == nil {
					ids[i] = o.ID
				}
			}()
		}
		wg.Wait()
		for _, id := range ids {
			if id == "" || id != ids[0] {
				t.Fatalf("concurrent renew requests must converge on one order: %v", ids)
			}
		}
		// 并发认领：两个 worker 只有一个拿到
		var got []*claimed
		var mu sync.Mutex
		for _, owner := range []string{"worker-a", "worker-b", "worker-c"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c, err := e.svc.claimOrder(e.ctx, e.tenant, owner)
				if err != nil {
					t.Error(err)
				}
				if c != nil {
					mu.Lock()
					got = append(got, c)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(got) != 1 || got[0].attempt != 1 {
			t.Fatalf("exactly one worker must win the claim, got %d", len(got))
		}
		// 租约过期后别的 worker 接手；原主人续租失败、落库被拒
		first := got[0]
		if _, err := e.admin.Exec(e.ctx, `UPDATE certificate_orders SET lease_until = now() - interval '1 second' WHERE id = $1`, first.id); err != nil {
			t.Fatal(err)
		}
		second, err := e.svc.claimOrder(e.ctx, e.tenant, "worker-d")
		if err != nil || second == nil || second.id != first.id || second.attempt != 2 {
			t.Fatalf("expired lease must be taken over: %+v err=%v", second, err)
		}
		if ok, err := e.svc.extendLease(e.ctx, e.tenant, first); err != nil || ok {
			t.Fatalf("old owner must lose the lease: ok=%v err=%v", ok, err)
		}
		if err := e.svc.finishFailure(e.ctx, e.tenant, first, failureOutcome{code: "x", countsAsFailure: true}); !errors.Is(err, errLeaseLost) {
			t.Fatalf("old owner must not write results, got %v", err)
		}
		if ok, err := e.svc.extendLease(e.ctx, e.tenant, second); err != nil || !ok {
			t.Fatalf("new owner keeps the lease: ok=%v err=%v", ok, err)
		}
	})

	t.Run("issue, renew with ARI replaces, refresh ARI", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 5)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "wild", cred.Credential.ID, "example.com", "*.example.com")
		e.run(t)
		detail, err := e.svc.CertificateDetail(e.ctx, e.tenant, cert.ID)
		if err != nil || detail.Certificate.Status != StatusActive || len(detail.Versions) != 1 || detail.Versions[0].CA != CACustom {
			t.Fatalf("after first run: %+v err=%v", detail, err)
		}
		// 私钥密文按 (租户, 证书, 版本) 绑定：解得开、是 PKCS#8、与链里的公钥一致
		var sealed []byte
		var chain, ari string
		if err := e.admin.QueryRow(e.ctx, `SELECT private_key_sealed, chain_pem, ari_cert_id FROM certificate_versions
			 WHERE certificate_id = $1 AND version = 1`, cert.ID).Scan(&sealed, &chain, &ari); err != nil {
			t.Fatal(err)
		}
		der, err := e.svc.sealer.Open(sealed, versionKeyAAD(e.tenant, cert.ID, 1))
		if err != nil {
			t.Fatalf("open private key: %v", err)
		}
		if _, err := e.svc.sealer.Open(sealed, versionKeyAAD(e.tenant, cert.ID, 2)); err == nil {
			t.Fatal("ciphertext must not open under another version's AAD")
		}
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := buildIssued(key.(crypto.Signer), []byte(chain), ""); err != nil {
			t.Fatalf("stored key does not match the stored chain: %v", err)
		}
		if n := e.scalar(t, `SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'certificate.issued'`, e.tenant); n.(int64) != 1 {
			t.Fatalf("issued audit rows = %v", n)
		}
		// 续期：到了续期时间，订单带上一版的 ARI certID
		if _, err := e.admin.Exec(e.ctx, `UPDATE certificates SET renew_after = now() - interval '1 minute' WHERE id = $1`, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		state, reason, _, replaces := e.order(t, cert.ID)
		_, _, sent := e.ca.stats()
		if state != "succeeded" || reason != "renewal" || replaces == nil || *replaces != ari || sent[len(sent)-1] != ari {
			t.Fatalf("renewal order state=%s reason=%s replaces=%v sent=%v want %s", state, reason, replaces, sent, ari)
		}
		if v := e.scalar(t, `SELECT is_renewal FROM certificate_versions WHERE certificate_id = $1 AND version = 2`, cert.ID); v != true {
			t.Fatalf("second version is_renewal = %v", v)
		}
		// ARI：刷新后窗口落库，续期时间在窗口内
		if err := e.svc.refreshARI(e.ctx, e.tenant, 5, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
		var start, end, renew *time.Time
		if err := e.admin.QueryRow(e.ctx, `SELECT ari_window_start, ari_window_end, renew_after FROM certificates WHERE id = $1`,
			cert.ID).Scan(&start, &end, &renew); err != nil {
			t.Fatal(err)
		}
		if start == nil || end == nil || renew == nil || renew.Before(start.Add(-time.Second)) || renew.After(*end) {
			t.Fatalf("ARI window=%v..%v renew_after=%v", start, end, renew)
		}
	})

	t.Run("CA 429 backs off by Retry-After", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 6)
		cred := e.credential(t, "cf", cfToken)
		e.ca.set429("3600")
		cert := e.certificate(t, "rl", cred.Credential.ID, "rl.example.com")
		e.run(t)
		state, _, code, _ := e.order(t, cert.ID)
		if state != "failed" || code == nil || *code != "rate_limited" {
			t.Fatalf("order state=%s code=%v", state, code)
		}
		var wait float64
		var failures int
		if err := e.admin.QueryRow(e.ctx, `SELECT extract(epoch FROM next_attempt_at - now()), consecutive_failures
			 FROM certificates WHERE id = $1`, cert.ID).Scan(&wait, &failures); err != nil {
			t.Fatal(err)
		}
		if wait < 3500 || wait > 3700 || failures != 0 {
			t.Fatalf("backoff %.0fs failures=%d, want ~3600s and not counted", wait, failures)
		}
		// 退避期间不再排单
		e.ca.set429("")
		e.run(t)
		if n := e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = $1`, cert.ID); n.(int64) != 1 {
			t.Fatalf("no new order during back-off, orders=%v", n)
		}
	})

	t.Run("rejected token never reaches the CA", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 7)
		bad := e.credential(t, "cf-bad", "revoked-token")
		if bad.OK || bad.Credential.VerifyStatus != "error" {
			t.Fatalf("bad token must fail verification: %+v", bad)
		}
		cert := e.certificate(t, "blocked", bad.Credential.ID, "blocked.example.com")
		before, _, _ := e.ca.stats()
		e.run(t)
		after, _, _ := e.ca.stats()
		state, _, code, _ := e.order(t, cert.ID)
		if after != before || state != "failed" || code == nil || *code != "credential_rejected" {
			t.Fatalf("CA requests %d→%d, order=%s code=%v", before, after, state, code)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = $1`, cert.ID); s != StatusBlockedCredential {
			t.Fatalf("certificate status = %v", s)
		}
		e.run(t) // 停在 blocked_credential：不再排单
		if n := e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = $1`, cert.ID); n.(int64) != 1 {
			t.Fatalf("blocked certificate must not queue again, orders=%v", n)
		}
		// 换成好令牌：校验通过即恢复，下一轮签出
		fixed, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, bad.Credential.ID,
			DNSCredentialPatch{Secret: map[string]string{"api_token": cfToken}})
		if err != nil || !fixed.OK || fixed.Resumed != 1 {
			t.Fatalf("fixing the token must resume the certificate: %+v err=%v", fixed, err)
		}
		e.run(t)
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = $1`, cert.ID); s != StatusActive {
			t.Fatalf("after fix status = %v", s)
		}
	})

	t.Run("local weekly limit blocks, ZeroSSL fallback issues", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 8)
		cred := e.credential(t, "cf", cfToken)
		// 造 50 张 7 天内签出的新证书（同一注册域），挂在一张暂停的证书上，worker 不碰它
		seed := e.certificate(t, "seed", cred.Credential.ID, "seed.example.com")
		if _, err := e.svc.SetCertificatePaused(e.ctx, e.tenant, e.actor, seed.ID, true); err != nil {
			t.Fatal(err)
		}
		var acct string
		if err := e.admin.QueryRow(e.ctx, `INSERT INTO acme_accounts (tenant_id, ca, directory_url, account_key_sealed, account_url)
			VALUES ($1, 'custom', 'https://seed.invalid/dir', '\x00', 'https://seed.invalid/acct/1') RETURNING id::text`,
			e.tenant).Scan(&acct); err != nil {
			t.Fatal(err)
		}
		if _, err := e.admin.Exec(e.ctx, `
			INSERT INTO certificate_versions (tenant_id, certificate_id, version, acme_account_id, ca, identifiers, is_renewal,
			  serial, chain_pem, private_key_sealed, public_key_sha256, chain_sha256, not_before, not_after, created_at)
			SELECT $1, $2, g, $3, 'custom', ARRAY['n' || g || '.example.com'], false, to_hex(g), 'seed', '\x00',
			       decode(repeat('00', 32), 'hex'), decode(repeat('00', 32), 'hex'), now(), now() + interval '90 days',
			       now() - make_interval(hours => g)
			  FROM generate_series(1, 50) g`, e.tenant, seed.ID, acct); err != nil {
			t.Fatal(err)
		}
		cert := e.certificate(t, "limited", cred.Credential.ID, "new.example.com")
		_, ordersBefore, _ := e.ca.stats()
		e.run(t)
		_, ordersAfter, _ := e.ca.stats()
		state, _, code, _ := e.order(t, cert.ID)
		if ordersAfter != ordersBefore || state != "failed" || code == nil || *code != "rate_limited_local" {
			t.Fatalf("new orders %d→%d, order=%s code=%v", ordersBefore, ordersAfter, state, code)
		}
		var wait float64
		if err := e.admin.QueryRow(e.ctx, `SELECT extract(epoch FROM next_attempt_at - now()) FROM certificates WHERE id = $1`,
			cert.ID).Scan(&wait); err != nil || wait < 5*86400 || wait > 7*86400 {
			t.Fatalf("local limit back-off = %.0fs err=%v, want until the oldest ages out", wait, err)
		}
		// 配上备用 CA（要 EAB 的第二个 pebble），手动续期：本地超限且从没签出过 → 改走 ZeroSSL
		zs := startCA(t, e.dns.addr, true)
		e.svc.opts.ZeroSSLDirectory = zs.dir
		hmac := zs.eabHMACB64
		if _, err := e.svc.SaveACMESettings(e.ctx, e.tenant, e.actor, ACMESettingsInput{ZeroSSLEnabled: true,
			ZeroSSLEABKID: zs.eabKeyID, ZeroSSLEABHMAC: &hmac}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RenewCertificate(e.ctx, e.tenant, e.actor, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if ca := e.scalar(t, `SELECT ca FROM certificate_versions WHERE certificate_id = $1 AND version = 1`, cert.ID); ca != CAZeroSSL {
			t.Fatalf("fallback issuance CA = %v", ca)
		}
		if kid := e.scalar(t, `SELECT eab_kid FROM acme_accounts WHERE tenant_id = $1 AND ca = 'zerossl'`, e.tenant); kid != zs.eabKeyID {
			t.Fatalf("zerossl account eab_kid = %v", kid)
		}
	})
}
