package service

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/repo"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// TestCertsPG 是真库 + 进程内 pebble/DNS/提供方模拟的 certs service 回归（由 TestCertsPG18 改写，无租户）。
func TestCertsPG(t *testing.T) {
	t.Run("immutable versions and append-only issuances", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "wild", cred.Credential.ID, "*.example.com")
		e.run(t)
		if v := e.scalar(t, `SELECT count(*) FROM certificate_versions WHERE certificate_id = ?`, cert.ID); v.(int64) != 1 {
			t.Fatalf("versions = %v", v)
		}
		if err := e.exec(e.ctx, `UPDATE certificate_versions SET serial = 'ff' WHERE certificate_id = ?`, cert.ID); err == nil ||
			!strings.Contains(err.Error(), "23001") {
			t.Fatalf("UPDATE certificate_versions must be denied, got %v", err)
		}
		if err := e.exec(e.ctx, `UPDATE certificate_issuances SET serial = 'ff' WHERE certificate_id = ?`, cert.ID); err == nil ||
			!strings.Contains(err.Error(), "23001") {
			t.Fatalf("UPDATE certificate_issuances must be denied, got %v", err)
		}
		if err := e.exec(e.ctx, `DELETE FROM certificate_issuances WHERE certificate_id = ?`, cert.ID); err == nil ||
			!strings.Contains(err.Error(), "23001") {
			t.Fatalf("DELETE certificate_issuances must be denied, got %v", err)
		}
		if err := e.exec(e.ctx, `UPDATE certificates SET current_version_id = NULL, not_before = NULL, not_after = NULL, status = 'pending' WHERE id = ?`, cert.ID); err != nil {
			t.Fatal(err)
		}
		// 与删证书同序：先删订单（订单引用版本），再删版本
		if err := e.exec(e.ctx, `DELETE FROM certificate_orders WHERE certificate_id = ?`, cert.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.exec(e.ctx, `DELETE FROM certificate_versions WHERE certificate_id = ?`, cert.ID); err != nil {
			t.Fatalf("DELETE certificate_versions after clearing current_version must be allowed, got %v", err)
		}
	})

	t.Run("secrets are write-only and kept when absent", func(t *testing.T) {
		e := newPGEnv(t)
		created := e.credential(t, "cf", cfToken)
		if !created.OK || created.Credential.VerifyStatus != "ok" || created.Credential.SecretHint != cfToken[len(cfToken)-4:] {
			t.Fatalf("create+verify = %+v", created)
		}
		id := created.Credential.ID
		secretOf := func() map[string]string {
			var sealed []byte
			if err := e.row(e.ctx, `SELECT secret_sealed FROM dns_credentials WHERE id = ?`, id).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			s, err := e.svc.openSecret(id, sealed)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		rename := "cf-renamed"
		if _, err := e.svc.UpdateDNSCredential(e.ctx, id, DNSCredentialPatch{Name: &rename}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.UpdateDNSCredential(e.ctx, id, DNSCredentialPatch{Secret: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		if got := secretOf()["api_token"]; got != cfToken {
			t.Fatalf("PATCH without the token must keep it, got %q", got)
		}
		if _, err := e.svc.UpdateDNSCredential(e.ctx, id, DNSCredentialPatch{Secret: map[string]string{"api_token": ""}}); err == nil {
			t.Fatal("explicit empty token must be rejected")
		}
		res, err := e.svc.UpdateDNSCredential(e.ctx, id, DNSCredentialPatch{Secret: map[string]string{"api_token": "rotated-token-9999"}})
		if err != nil || res.OK || res.Credential.VerifyStatus != "error" || secretOf()["api_token"] != "rotated-token-9999" {
			t.Fatalf("rotated to a token the provider rejects: res=%+v err=%v", res, err)
		}
		hmac := "c2VjcmV0LWhtYWMta2V5"
		if _, err := e.svc.SaveACMESettings(e.ctx, ACMESettingsInput{ContactEmail: "ops@example.com",
			ZeroSSLEnabled: true, ZeroSSLEABKID: "kid-1", ZeroSSLEABHMAC: &hmac}); err != nil {
			t.Fatal(err)
		}
		if s, err := e.svc.SaveACMESettings(e.ctx, ACMESettingsInput{ContactEmail: "ops@example.com",
			ZeroSSLEnabled: true, ZeroSSLEABKID: "kid-1"}); err != nil || !s.ZeroSSLEABHMACSet {
			t.Fatalf("absent HMAC must be kept: %+v %v", s, err)
		}
		cfg, err := e.svc.loadACMEConfig(e.ctx, true)
		if err != nil || cfg.eabHMAC != hmac {
			t.Fatalf("stored HMAC = %q err=%v", cfg.eabHMAC, err)
		}
		empty := ""
		if _, err := e.svc.SaveACMESettings(e.ctx, ACMESettingsInput{ZeroSSLEnabled: true,
			ZeroSSLEABKID: "kid-1", ZeroSSLEABHMAC: &empty}); err == nil {
			t.Fatal("clearing the HMAC while ZeroSSL stays enabled must be rejected")
		}
		if s, err := e.svc.SaveACMESettings(e.ctx, ACMESettingsInput{ZeroSSLEABHMAC: &empty}); err != nil || s.ZeroSSLEABHMACSet {
			t.Fatalf("explicit empty HMAC clears it: %+v %v", s, err)
		}
		creds, _ := e.svc.ListDNSCredentials(e.ctx)
		settings, _ := e.svc.ACMESettings(e.ctx)
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
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "one", cred.Credential.ID, "one.example.com")
		if err := e.exec(e.ctx, `INSERT INTO certificate_orders (certificate_id, reason) VALUES (?, 'manual')`, cert.ID); err == nil ||
			!strings.Contains(err.Error(), "certificate_orders_one_active") {
			t.Fatalf("second active order must hit partial unique index, got %v", err)
		}
		var wg sync.WaitGroup
		ids := make([]uuid.UUID, 8)
		for i := range ids {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				o, err := e.svc.RenewCertificate(e.ctx, cert.ID)
				if err == nil {
					ids[i] = o.ID
				}
			}(i)
		}
		wg.Wait()
		for _, id := range ids {
			if id == uuid.Nil || id != ids[0] {
				t.Fatalf("concurrent renew requests must converge on one order: %v", ids)
			}
		}
		var got []repo.ClaimedOrder
		var mu sync.Mutex
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var orderID uuid.UUID
				if err := e.row(e.ctx, `SELECT id FROM certificate_orders WHERE certificate_id = ? AND state = 'queued' LIMIT 1`,
					cert.ID).Scan(&orderID); err != nil {
					return
				}
				c, ok, err := e.svc.claimOrder(e.ctx, orderID)
				if err != nil {
					t.Error(err)
					return
				}
				if ok {
					mu.Lock()
					got = append(got, c)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(got) != 1 || got[0].Attempt != 1 {
			t.Fatalf("exactly one worker must win the claim, got %d", len(got))
		}
		first := got[0]
		if err := e.exec(e.ctx, `UPDATE certificate_orders SET lease_until = now() - interval '1 second' WHERE id = ?`, first.ID); err != nil {
			t.Fatal(err)
		}
		c2, ok, err := e.svc.claimOrder(e.ctx, first.ID)
		if err != nil || ok || c2.ID != uuid.Nil {
			t.Fatalf("expired lease must not be re-claimed via ClaimOrder: %+v ok=%v err=%v", c2, ok, err)
		}
		e.svc.lastSweep.Store(time.Now().UnixNano())
		if _, err := e.svc.Patrol(e.ctx); err != nil {
			t.Fatal(err)
		}
		state, _, code, _ := e.order(t, cert.ID)
		if state != "failed" || code == nil || *code != "interrupted" {
			t.Fatalf("patrol must fail interrupted order: state=%s code=%v", state, code)
		}
		if f := e.scalar(t, `SELECT consecutive_failures FROM certificates WHERE id = ?`, cert.ID); f.(int64) != 0 {
			t.Fatalf("interrupted orders must not count as failures, consecutive_failures = %v", f)
		}
		if err := e.svc.finishFailure(e.ctx, first, failureOutcome{code: "x", countsAsFailure: true}); !errors.Is(err, errLeaseLost) {
			t.Fatalf("old owner must not write results, got %v", err)
		}
	})

	t.Run("issue, renew with ARI replaces, refresh ARI", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "wild", cred.Credential.ID, "example.com", "*.example.com")
		e.run(t)
		detail, err := e.svc.CertificateDetail(e.ctx, cert.ID)
		if err != nil || detail.Certificate.Status != StatusActive || len(detail.Versions) != 1 || detail.Versions[0].CA != CACustom {
			t.Fatalf("after first run: %+v err=%v", detail, err)
		}
		var sealed []byte
		var chain, ari string
		if err := e.row(e.ctx, `SELECT private_key_sealed, chain_pem, ari_cert_id FROM certificate_versions
			 WHERE certificate_id = ? AND version = 1`, cert.ID).Scan(&sealed, &chain, &ari); err != nil {
			t.Fatal(err)
		}
		e.ca.settle(t, e.serial(t, cert.ID, 1), "")
		der, err := e.svc.sealer.Open(sealed, versionKeyAAD(cert.ID, 1))
		if err != nil {
			t.Fatalf("open private key: %v", err)
		}
		if _, err := e.svc.sealer.Open(sealed, versionKeyAAD(cert.ID, 2)); err == nil {
			t.Fatal("ciphertext must not open under another version's AAD")
		}
		key, err := x509.ParsePKCS8PrivateKey(der)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := buildIssued(key.(crypto.Signer), []byte(chain), ""); err != nil {
			t.Fatalf("stored key does not match the stored chain: %v", err)
		}
		if n := e.scalar(t, `SELECT count(*) FROM audit_events WHERE action = 'certificate.issued' AND resource_id = ?`, cert.ID); n.(int64) != 1 {
			t.Fatalf("issued audit rows = %v", n)
		}
		if err := e.exec(e.ctx, `UPDATE certificates SET renew_after = now() - interval '1 minute' WHERE id = ?`, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		state, reason, _, replaces := e.order(t, cert.ID)
		_, _, sent := e.ca.stats()
		if state != "succeeded" || reason != "renewal" || replaces == nil || *replaces != ari || len(sent) == 0 || sent[len(sent)-1] != ari {
			t.Fatalf("renewal order state=%s reason=%s replaces=%v sent=%v want %s", state, reason, replaces, sent, ari)
		}
		e.ca.settle(t, e.serial(t, cert.ID, 2), e.serial(t, cert.ID, 1))
		if v := e.scalar(t, `SELECT is_renewal FROM certificate_versions WHERE certificate_id = ? AND version = 2`, cert.ID); v != true {
			t.Fatalf("second version is_renewal = %v", v)
		}
		e.svc.lastSweep.Store(0)
		if _, err := e.svc.refreshARI(e.ctx); err != nil {
			t.Fatal(err)
		}
		var start, end, renew *time.Time
		if err := e.row(e.ctx, `SELECT ari_window_start, ari_window_end, renew_after FROM certificates WHERE id = ?`,
			cert.ID).Scan(&start, &end, &renew); err != nil {
			t.Fatal(err)
		}
		if start == nil || end == nil || renew == nil || renew.Before(start.Add(-time.Second)) || renew.After(*end) {
			t.Fatalf("ARI window=%v..%v renew_after=%v", start, end, renew)
		}
	})

	t.Run("CA 429 backs off by Retry-After", func(t *testing.T) {
		e := newPGEnv(t)
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
		if err := e.row(e.ctx, `SELECT extract(epoch FROM next_attempt_at - now()), consecutive_failures
			 FROM certificates WHERE id = ?`, cert.ID).Scan(&wait, &failures); err != nil {
			t.Fatal(err)
		}
		if wait < 3500 || wait > 3700 || failures != 0 {
			t.Fatalf("backoff %.0fs failures=%d, want ~3600s and not counted", wait, failures)
		}
		e.ca.set429("")
		e.run(t)
		if n := e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = ?`, cert.ID); n.(int64) != 1 {
			t.Fatalf("no new order during back-off, orders=%v", n)
		}
	})

	t.Run("rejected token never reaches the CA", func(t *testing.T) {
		e := newPGEnv(t)
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
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = ?`, cert.ID); s != StatusBlockedCredential {
			t.Fatalf("certificate status = %v", s)
		}
		e.run(t)
		if n := e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = ?`, cert.ID); n.(int64) != 1 {
			t.Fatalf("blocked certificate must not queue again, orders=%v", n)
		}
		fixed, err := e.svc.UpdateDNSCredential(e.ctx, bad.Credential.ID,
			DNSCredentialPatch{Secret: map[string]string{"api_token": cfToken}})
		if err != nil || !fixed.OK || fixed.Resumed != 1 {
			t.Fatalf("fixing the token must resume the certificate: %+v err=%v", fixed, err)
		}
		e.run(t)
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = ?`, cert.ID); s != StatusActive {
			t.Fatalf("after fix status = %v", s)
		}
	})

	t.Run("local weekly limit blocks, ZeroSSL fallback issues", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		if err := e.exec(e.ctx, `
			INSERT INTO certificate_issuances (certificate_id, ca, identifiers, is_renewal, serial, created_at)
			SELECT uuidv7(), CASE WHEN g % 2 = 0 THEN 'custom' ELSE 'zerossl' END,
			       jsonb_build_array('n' || g || '.example.com'), false, to_hex(g),
			       now() - make_interval(hours => g)
			  FROM generate_series(1, 50) g`); err != nil {
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
		if err := e.row(e.ctx, `SELECT extract(epoch FROM next_attempt_at - now()) FROM certificates WHERE id = ?`,
			cert.ID).Scan(&wait); err != nil || wait < 117*3600 || wait > 119*3600 {
			t.Fatalf("local limit back-off = %.0fs err=%v, want ~118h until the oldest ages out", wait, err)
		}
		zs := startCA(t, e.dns.addr, true)
		e.svc.opts.ZeroSSLDirectory = zs.dir
		hmac := zs.eabHMACB64
		if _, err := e.svc.SaveACMESettings(e.ctx, ACMESettingsInput{ZeroSSLEnabled: true,
			ZeroSSLEABKID: zs.eabKeyID, ZeroSSLEABHMAC: &hmac}); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RenewCertificate(e.ctx, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if ca := e.scalar(t, `SELECT ca FROM certificate_versions WHERE certificate_id = ? AND version = 1`, cert.ID); ca != CAZeroSSL {
			t.Fatalf("fallback issuance CA = %v", ca)
		}
		if kid := e.scalar(t, `SELECT eab_kid FROM acme_accounts WHERE ca = 'zerossl'`); kid != zs.eabKeyID {
			t.Fatalf("zerossl account eab_kid = %v", kid)
		}
	})

	t.Run("patrol enqueues certs:issue once per order", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "q", cred.Credential.ID, "q.example.com")
		var orderID uuid.UUID
		if err := e.row(e.ctx, `SELECT id FROM certificate_orders WHERE certificate_id = ? AND state = 'queued'`, cert.ID).
			Scan(&orderID); err != nil {
			t.Fatal(err)
		}
		fq := &fakeIssueQueue{}
		e.svc.queue = fq
		e.svc.lastSweep.Store(time.Now().UnixNano())
		res, err := e.svc.Patrol(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res.Enqueued != 1 || len(fq.calls) != 1 {
			t.Fatalf("enqueued=%d calls=%d", res.Enqueued, len(fq.calls))
		}
		c := fq.calls[0]
		if c.task.Type() != TaskIssue {
			t.Fatalf("task type = %s", c.task.Type())
		}
		var tid string
		for _, o := range c.opts {
			if o.Type() == asynq.TaskIDOpt {
				tid = o.Value().(string)
			}
			if o.Type() == asynq.TimeoutOpt && o.Value().(time.Duration) != IssueTimeout {
				t.Fatalf("timeout = %v", o.Value())
			}
			if o.Type() == asynq.MaxRetryOpt && o.Value().(int) != 0 {
				t.Fatalf("max retry = %v", o.Value())
			}
		}
		if tid != IssueTaskID(orderID) {
			t.Fatalf("task id = %q want %q", tid, IssueTaskID(orderID))
		}
		fq.conflict = true
		res2, err := e.svc.Patrol(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res2.Enqueued != 0 {
			t.Fatalf("duplicate task id must not error and Enqueued=0, got %d", res2.Enqueued)
		}
	})
}

type fakeIssueQueue struct {
	conflict bool
	calls    []fakeIssueCall
}

type fakeIssueCall struct {
	task *asynq.Task
	opts []asynq.Option
}

func (q *fakeIssueQueue) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if q.conflict {
		return nil, asynq.ErrTaskIDConflict
	}
	q.calls = append(q.calls, fakeIssueCall{task: task, opts: opts})
	return &asynq.TaskInfo{}, nil
}

var _ Queue = (*fakeIssueQueue)(nil)
