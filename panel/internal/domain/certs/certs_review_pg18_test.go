package certs

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 对抗审查（10-08）的回归用例，与 TestCertsPG18 同库（certs 域），租户 id 接着用 ce57 前缀。
func TestCertsReviewPG18(t *testing.T) {
	_, admin, app := pg18test.Open(t, certsFixture)

	activeOrders := func(e *pgEnv, certID string) int64 {
		return e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = $1 AND state IN ('queued', 'running')`,
			certID).(int64)
	}

	t.Run("a stale preflight verdict does not block a rotated credential", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 11)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "stale", cred.Credential.ID, "stale.example.com")
		c, err := e.svc.claimOrder(e.ctx, e.tenant, "worker-a")
		if err != nil || c == nil {
			t.Fatalf("claim: %v %v", c, err)
		}
		// worker 拿旧令牌预检失败的同时，管理员换了令牌（新令牌校验通过，row_version 前进）
		rotated, err := e.svc.UpdateDNSCredential(e.ctx, e.tenant, e.actor, cred.Credential.ID,
			DNSCredentialPatch{Secret: map[string]string{"api_token": cfToken}})
		if err != nil || !rotated.OK || rotated.Credential.RowVersion == cred.Credential.RowVersion {
			t.Fatalf("rotate: %+v %v", rotated, err)
		}
		if err := e.svc.finishFailure(e.ctx, e.tenant, c, failureOutcome{code: "credential_rejected", detail: "旧令牌被拒",
			blockCredential: true, credRowVersion: cred.Credential.RowVersion}); err != nil {
			t.Fatal(err)
		}
		if v := e.scalar(t, `SELECT verify_status FROM dns_credentials WHERE id = $1`, cred.Credential.ID); v != "ok" {
			t.Fatalf("rotated credential knocked back to %v by a stale verdict", v)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = $1`, cert.ID); s != StatusPending {
			t.Fatalf("certificate blocked by a stale verdict: %v", s)
		}
		// 同一版凭据的结论照常拦下
		if _, err := e.svc.RenewCertificate(e.ctx, e.tenant, e.actor, cert.ID); err != nil {
			t.Fatal(err)
		}
		c2, err := e.svc.claimOrder(e.ctx, e.tenant, "worker-b")
		if err != nil || c2 == nil {
			t.Fatalf("claim again: %v %v", c2, err)
		}
		if err := e.svc.finishFailure(e.ctx, e.tenant, c2, failureOutcome{code: "credential_rejected", detail: "令牌被拒",
			blockCredential: true, credRowVersion: rotated.Credential.RowVersion}); err != nil {
			t.Fatal(err)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = $1`, cert.ID); s != StatusBlockedCredential {
			t.Fatalf("current verdict must block: %v", s)
		}
	})

	t.Run("the issuance log outlives the certificate and is append-only", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 12)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "dup", cred.Credential.ID, "dup.example.com")
		e.run(t)
		if err := e.svc.DeleteCertificate(e.ctx, e.tenant, e.actor, cert.ID); err != nil {
			t.Fatal(err)
		}
		if n := e.scalar(t, `SELECT count(*) FROM certificate_versions WHERE certificate_id = $1`, cert.ID); n.(int64) != 0 {
			t.Fatalf("versions left after delete: %v", n)
		}
		var recent []recentIssuance
		if err := app.InTx(e.ctx, platformdb.Scope{TenantID: e.tenant}, func(tx pgx.Tx) error {
			var err error
			recent, _, err = loadRecentIssuances(e.ctx, tx, e.tenant)
			return err
		}); err != nil || len(recent) != 1 || recent[0].ca != CACustom || recent[0].identifiers[0] != "dup.example.com" {
			t.Fatalf("issuance log after delete = %+v err=%v", recent, err)
		}
		for priv, want := range map[string]bool{"INSERT": true, "SELECT": true, "UPDATE": false, "DELETE": false} {
			if got := e.scalar(t, `SELECT has_table_privilege('aegis_app', 'public.certificate_issuances', $1)`, priv); got.(bool) != want {
				t.Errorf("aegis_app %s on certificate_issuances = %v, want %v", priv, got, want)
			}
		}
		err := app.InTx(e.ctx, platformdb.Scope{TenantID: e.tenant}, func(tx pgx.Tx) error {
			_, err := tx.Exec(e.ctx, `DELETE FROM certificate_issuances WHERE tenant_id = $1`, e.tenant)
			return err
		})
		if !platformdb.IsInsufficientPrivilege(err) {
			t.Fatalf("aegis_app must not delete the issuance log, got %v", err)
		}
	})

	t.Run("resume leaves an issued certificate on its renewal schedule", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 13)
		cred := e.credential(t, "cf", cfToken)
		issued := e.certificate(t, "issued", cred.Credential.ID, "issued.example.com")
		e.run(t)
		for _, paused := range []bool{true, false} {
			if _, err := e.svc.SetCertificatePaused(e.ctx, e.tenant, e.actor, issued.ID, paused); err != nil {
				t.Fatal(err)
			}
		}
		if n := activeOrders(e, issued.ID); n != 0 {
			t.Fatalf("resuming an issued certificate must not queue an order, got %d", n)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = $1`, issued.ID); s != StatusActive {
			t.Fatalf("status after resume = %v", s)
		}
		fresh := e.certificate(t, "fresh", cred.Credential.ID, "fresh.example.com")
		for _, paused := range []bool{true, false} {
			if _, err := e.svc.SetCertificatePaused(e.ctx, e.tenant, e.actor, fresh.ID, paused); err != nil {
				t.Fatal(err)
			}
		}
		if n := activeOrders(e, fresh.ID); n != 1 {
			t.Fatalf("resuming a never-issued certificate must queue it right away, got %d", n)
		}
	})

	t.Run("a failure after an identifier change queues the new names", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 14)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "moving", cred.Credential.ID, "old.example.com")
		c, err := e.svc.claimOrder(e.ctx, e.tenant, "worker-a")
		if err != nil || c == nil {
			t.Fatalf("claim: %v %v", c, err)
		}
		w, err := e.svc.loadOrderWork(e.ctx, e.tenant, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.UpdateCertificate(e.ctx, e.tenant, e.actor, cert.ID, CertificatePatch{Identifiers: []string{"new.example.com"}}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.finishFailure(e.ctx, e.tenant, c, failureOutcome{code: "acme_error", countsAsFailure: true, work: w}); err != nil {
			t.Fatal(err)
		}
		state, reason, _, _ := e.order(t, cert.ID)
		if state != "queued" || reason != "manual" {
			t.Fatalf("latest order state=%s reason=%s, want a queued manual order for the new names", state, reason)
		}
	})

	t.Run("delete refuses while an order is being issued", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 15)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "busy", cred.Credential.ID, "busy.example.com")
		if c, err := e.svc.claimOrder(e.ctx, e.tenant, "worker-a"); err != nil || c == nil {
			t.Fatalf("claim: %v %v", c, err)
		}
		var he *httpx.Error
		if err := e.svc.DeleteCertificate(e.ctx, e.tenant, e.actor, cert.ID); !errors.As(err, &he) || he.Code != httpx.CodeConflict {
			t.Fatalf("delete during issuance must be 409, got %v", err)
		}
	})

	t.Run("a deactivated ACME account is replaced on the next order", func(t *testing.T) {
		e := newPGEnv(t, admin, app, 16)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "acct", cred.Credential.ID, "acct.example.com")
		e.run(t)
		var acct string
		if err := e.admin.QueryRow(e.ctx, `SELECT id::text FROM acme_accounts WHERE tenant_id = $1`, e.tenant).Scan(&acct); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.deactivateAccount(e.ctx, e.tenant, acct); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RenewCertificate(e.ctx, e.tenant, e.actor, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		if state, _, code, _ := e.order(t, cert.ID); state != "succeeded" {
			t.Fatalf("renewal after deactivation: %s %v", state, code)
		}
		var total, valid int64
		if err := e.admin.QueryRow(e.ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'valid') FROM acme_accounts
			 WHERE tenant_id = $1`, e.tenant).Scan(&total, &valid); err != nil || total != 2 || valid != 1 {
			t.Fatalf("accounts total=%d valid=%d err=%v, want a fresh registration next to the deactivated one", total, valid, err)
		}
	})
}
