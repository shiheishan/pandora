package service

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// TestCertsReviewPG 对抗审查回归（由 TestCertsReviewPG18 改写）。
func TestCertsReviewPG(t *testing.T) {
	activeOrders := func(e *pgEnv, certID uuid.UUID) int64 {
		return e.scalar(t, `SELECT count(*) FROM certificate_orders WHERE certificate_id = ? AND state IN ('queued', 'running')`,
			certID).(int64)
	}

	t.Run("a stale preflight verdict does not block a rotated credential", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "stale", cred.Credential.ID, "stale.example.com")
		c := e.claim(t, cert.ID)
		rotated, err := e.svc.UpdateDNSCredential(e.ctx, cred.Credential.ID,
			DNSCredentialPatch{Secret: map[string]string{"api_token": cfToken}})
		if err != nil || !rotated.OK || rotated.Credential.RowVersion == cred.Credential.RowVersion {
			t.Fatalf("rotate: %+v %v", rotated, err)
		}
		if err := e.svc.finishFailure(e.ctx, c, failureOutcome{code: "credential_rejected", detail: "旧令牌被拒",
			blockCredential: true, credRowVersion: cred.Credential.RowVersion}); err != nil {
			t.Fatal(err)
		}
		if v := e.scalar(t, `SELECT verify_status FROM dns_credentials WHERE id = ?`, cred.Credential.ID); v != "ok" {
			t.Fatalf("rotated credential knocked back to %v by a stale verdict", v)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = ?`, cert.ID); s != StatusPending {
			t.Fatalf("certificate blocked by a stale verdict: %v", s)
		}
		if _, err := e.svc.RenewCertificate(e.ctx, cert.ID); err != nil {
			t.Fatal(err)
		}
		c2 := e.claim(t, cert.ID)
		if err := e.svc.finishFailure(e.ctx, c2, failureOutcome{code: "credential_rejected", detail: "令牌被拒",
			blockCredential: true, credRowVersion: rotated.Credential.RowVersion}); err != nil {
			t.Fatal(err)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = ?`, cert.ID); s != StatusBlockedCredential {
			t.Fatalf("current verdict must block: %v", s)
		}
	})

	t.Run("the issuance log outlives the certificate and is append-only", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "dup", cred.Credential.ID, "dup.example.com")
		e.run(t)
		if err := e.svc.DeleteCertificate(e.ctx, cert.ID); err != nil {
			t.Fatal(err)
		}
		if n := e.scalar(t, `SELECT count(*) FROM certificate_versions WHERE certificate_id = ?`, cert.ID); n.(int64) != 0 {
			t.Fatalf("versions left after delete: %v", n)
		}
		recent, _, err := e.svc.loadRecentIssuances(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var found int
		for _, r := range recent {
			if len(r.identifiers) == 1 && r.identifiers[0] == "dup.example.com" && r.ca == CACustom {
				found++
			}
		}
		if found != 1 {
			t.Fatalf("issuance log after delete: found %d matching rows in recent=%d err=%v", found, len(recent), err)
		}
		if err := e.exec(e.ctx, `DELETE FROM certificate_issuances`); err == nil || !strings.Contains(err.Error(), "23001") {
			t.Fatalf("DELETE certificate_issuances must be denied, got %v", err)
		}
	})

	t.Run("resume leaves an issued certificate on its renewal schedule", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		issued := e.certificate(t, "issued", cred.Credential.ID, "issued.example.com")
		e.run(t)
		for _, paused := range []bool{true, false} {
			if _, err := e.svc.SetCertificatePaused(e.ctx, issued.ID, paused); err != nil {
				t.Fatal(err)
			}
		}
		if n := activeOrders(e, issued.ID); n != 0 {
			t.Fatalf("resuming an issued certificate must not queue an order, got %d", n)
		}
		if s := e.scalar(t, `SELECT status FROM certificates WHERE id = ?`, issued.ID); s != StatusActive {
			t.Fatalf("status after resume = %v", s)
		}
		fresh := e.certificate(t, "fresh", cred.Credential.ID, "fresh.example.com")
		for _, paused := range []bool{true, false} {
			if _, err := e.svc.SetCertificatePaused(e.ctx, fresh.ID, paused); err != nil {
				t.Fatal(err)
			}
		}
		if n := activeOrders(e, fresh.ID); n != 1 {
			t.Fatalf("resuming a never-issued certificate must queue it right away, got %d", n)
		}
	})

	t.Run("a failure after an identifier change queues the new names", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "moving", cred.Credential.ID, "old.example.com")
		c := e.claim(t, cert.ID)
		w, err := e.svc.loadOrderWork(e.ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.UpdateCertificate(e.ctx, cert.ID, CertificatePatch{Identifiers: []string{"new.example.com"}}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.finishFailure(e.ctx, c, failureOutcome{code: "acme_error", countsAsFailure: true, work: w}); err != nil {
			t.Fatal(err)
		}
		state, reason, _, _ := e.order(t, cert.ID)
		if state != "queued" || reason != "manual" {
			t.Fatalf("latest order state=%s reason=%s, want a queued manual order for the new names", state, reason)
		}
	})

	t.Run("delete refuses while an order is being issued", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "busy", cred.Credential.ID, "busy.example.com")
		_ = e.claim(t, cert.ID)
		delErr := e.svc.DeleteCertificate(e.ctx, cert.ID)
		if delErr == nil {
			t.Fatal("delete during issuance must fail")
		}
		if ae := errs.As(delErr); ae == nil || ae.Code != errs.Conflict {
			t.Fatalf("delete during issuance must be Conflict, got %v", delErr)
		}
	})

	t.Run("a deactivated ACME account is replaced on the next order", func(t *testing.T) {
		e := newPGEnv(t)
		cred := e.credential(t, "cf", cfToken)
		cert := e.certificate(t, "acct", cred.Credential.ID, "acct.example.com")
		e.run(t)
		var acct uuid.UUID
		if err := e.row(e.ctx, `SELECT id FROM acme_accounts LIMIT 1`).Scan(&acct); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.deactivateAccount(e.ctx, acct); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RenewCertificate(e.ctx, cert.ID); err != nil {
			t.Fatal(err)
		}
		e.run(t)
		state, _, _, replaces := e.order(t, cert.ID)
		if state != "succeeded" || replaces != nil {
			t.Fatalf("renewal after deactivation: state=%s replaces=%v, want success without replaces (new account)",
				state, derefStr(replaces))
		}
		var total, valid int64
		if err := e.row(e.ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'valid') FROM acme_accounts`).
			Scan(&total, &valid); err != nil || total != 2 || valid != 1 {
			t.Fatalf("accounts total=%d valid=%d err=%v, want a fresh registration next to the deactivated one", total, valid, err)
		}
	})
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
