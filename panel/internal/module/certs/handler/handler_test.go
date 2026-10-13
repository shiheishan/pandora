package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeSvc struct {
	Service
	list         service.CertificateList
	createIn     service.CertificateInput
	lastPatch    service.CertificatePatch
	lastPatchID  uuid.UUID
	lastDeleteID uuid.UUID
	lastRenewID  uuid.UUID
	lastPaused   bool
	lastPausedID uuid.UUID
	createCredIn service.DNSCredentialInput
	lastACMEIn   service.ACMESettingsInput
	renewOut     service.OrderBrief
	cert         service.Certificate
}

func (f *fakeSvc) ListCertificates(context.Context) (service.CertificateList, error) {
	return f.list, nil
}

func (f *fakeSvc) CreateCertificate(_ context.Context, in service.CertificateInput) (service.Certificate, error) {
	f.createIn = in
	if f.cert.ID != uuid.Nil {
		return f.cert, nil
	}
	return service.Certificate{ID: uuid.New(), Name: in.Name, Identifiers: in.Identifiers, Status: service.StatusPending}, nil
}

func (f *fakeSvc) UpdateCertificate(_ context.Context, id uuid.UUID, p service.CertificatePatch) (service.Certificate, error) {
	f.lastPatchID = id
	f.lastPatch = p
	return f.cert, nil
}

func (f *fakeSvc) DeleteCertificate(_ context.Context, id uuid.UUID) error {
	f.lastDeleteID = id
	return nil
}

func (f *fakeSvc) RenewCertificate(_ context.Context, id uuid.UUID) (service.OrderBrief, error) {
	f.lastRenewID = id
	if f.renewOut.ID != uuid.Nil {
		return f.renewOut, nil
	}
	return service.OrderBrief{ID: uuid.New(), State: service.OrderQueued, Reason: service.ReasonManual, CreatedAt: time.Now().UTC()}, nil
}

func (f *fakeSvc) SetCertificatePaused(_ context.Context, id uuid.UUID, paused bool) (service.Certificate, error) {
	f.lastPausedID = id
	f.lastPaused = paused
	return f.cert, nil
}

func (f *fakeSvc) CreateDNSCredential(_ context.Context, in service.DNSCredentialInput) (service.VerifyResult, error) {
	f.createCredIn = in
	return service.VerifyResult{OK: true, Warnings: nil}, nil
}

func (f *fakeSvc) SaveACMESettings(_ context.Context, in service.ACMESettingsInput) (service.ACMESettings, error) {
	f.lastACMEIn = in
	return service.ACMESettings{ContactEmail: in.ContactEmail}, nil
}

func newEngine(svc Service) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	e.Use(func(c *gin.Context) {
		authn.Set(c, authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin})
		c.Next()
	})
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: pass,
		Idempotency:  func(string) gin.HandlerFunc { return pass },
	}
	New(svc).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func do(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func jsonKeys(t *testing.T, v any) ([]string, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, m
}

func expectKeys(t *testing.T, got []string, want []string) {
	t.Helper()
	w := append([]string(nil), want...)
	sort.Strings(w)
	if strings.Join(got, ",") != strings.Join(w, ",") {
		t.Fatalf("JSON 键\n%v\n期望\n%v", got, w)
	}
}

// 键表照抄 frontend schemas.ts。
var (
	certKeys = []string{
		"id", "name", "identifiers", "wildcard", "challenge", "dns_credential_id", "dns_credential_name",
		"dns_provider", "key_type", "status", "paused_reason", "current_version", "current_ca", "not_before",
		"not_after", "renew_after", "ari_window_start", "ari_window_end", "next_attempt_at", "consecutive_failures",
		"last_error_code", "last_error", "expiry_level", "active_order", "row_version", "created_at", "updated_at",
	}
	summaryKeys = []string{"total", "warning", "critical", "expired", "failing", "paused", "blocked"}
	versionKeys = []string{
		"id", "version", "ca", "identifiers", "is_renewal", "serial", "not_before", "not_after",
		"chain_sha256", "public_key_sha256", "current", "created_at",
	}
	orderKeys = []string{
		"id", "reason", "state", "ca", "replaces", "attempt", "error_code", "error_detail",
		"version", "created_at", "started_at", "finished_at",
	}
	detailKeys        = []string{"certificate", "versions", "orders"}
	orderBriefKeys    = []string{"id", "state", "reason", "created_at"}
	dnsCredentialKeys = []string{
		"id", "name", "provider", "zone", "secret_hint", "verify_status", "verified_at", "verify_error",
		"visible_zones", "certificate_count", "row_version", "created_at", "updated_at",
	}
	verifyResultKeys = []string{"credential", "ok", "message", "warnings", "resumed"}
	acmeSettingsKeys = []string{
		"contact_email", "use_staging", "zerossl_enabled", "zerossl_eab_kid",
		"zerossl_eab_hmac_set", "directory_override",
	}
)

func TestCertificateResponseKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	credID := uuid.New()
	ob := orderBriefFrom(service.OrderBrief{ID: uuid.New(), State: service.OrderQueued, Reason: service.ReasonInitial, CreatedAt: now})
	keys, m := jsonKeys(t, certificateFrom(service.Certificate{
		ID: credID, Name: "x", Identifiers: nil, Wildcard: false, Challenge: "dns-01",
		DNSCredentialID: credID, DNSCredentialName: "c", DNSProvider: "cloudflare", KeyType: service.KeyECDSAP256,
		Status: service.StatusPending, ExpiryLevel: service.ExpiryNone, RowVersion: 1, CreatedAt: now, UpdatedAt: now,
		ActiveOrder: &service.OrderBrief{ID: uuid.New(), State: service.OrderRunning, Reason: service.ReasonInitial, CreatedAt: now},
	}))
	expectKeys(t, keys, certKeys)
	if m["paused_reason"] != nil || m["current_version"] != nil || m["active_order"] == nil {
		t.Fatalf("空值形状 %v", m)
	}
	ids, _ := m["identifiers"].([]any)
	if ids == nil {
		t.Fatalf("identifiers 应为 []")
	}
	_ = ob
}

func TestCertificateSummaryKeys(t *testing.T) {
	keys, _ := jsonKeys(t, summaryFrom(service.CertificateSummary{}))
	expectKeys(t, keys, summaryKeys)
}

func TestVersionResponseKeys(t *testing.T) {
	now := time.Now().UTC()
	keys, m := jsonKeys(t, versionFrom(service.Version{
		ID: uuid.New(), Version: 1, CA: "letsencrypt", Identifiers: nil, Serial: "s",
		NotBefore: now, NotAfter: now, ChainSHA256: "a", PublicKeySHA256: "b", CreatedAt: now,
	}))
	expectKeys(t, keys, versionKeys)
	ids, _ := m["identifiers"].([]any)
	if ids == nil {
		t.Fatalf("identifiers 应为 []")
	}
}

func TestOrderResponseKeys(t *testing.T) {
	now := time.Now().UTC()
	keys, m := jsonKeys(t, orderFrom(service.Order{
		ID: uuid.New(), Reason: service.ReasonRenewal, State: service.OrderFailed, Replaces: true, Attempt: 1, CreatedAt: now,
	}))
	expectKeys(t, keys, orderKeys)
	if m["ca"] != nil || m["version"] != nil {
		t.Fatalf("nullable %v", m)
	}
}

func TestCertificateDetailKeys(t *testing.T) {
	now := time.Now().UTC()
	keys, m := jsonKeys(t, detailFrom(service.CertificateDetail{
		Certificate: service.Certificate{ID: uuid.New(), DNSCredentialID: uuid.New(), CreatedAt: now, UpdatedAt: now},
		Versions:    nil,
		Orders:      nil,
	}))
	expectKeys(t, keys, detailKeys)
	vers, _ := m["versions"].([]any)
	ords, _ := m["orders"].([]any)
	if vers == nil || ords == nil {
		t.Fatalf("versions/orders 应为 []: %v", m)
	}
}

func TestOrderBriefKeys(t *testing.T) {
	keys, _ := jsonKeys(t, orderBriefFrom(service.OrderBrief{
		ID: uuid.New(), State: service.OrderQueued, Reason: service.ReasonManual, CreatedAt: time.Now().UTC(),
	}))
	expectKeys(t, keys, orderBriefKeys)
}

func TestDNSCredentialKeys(t *testing.T) {
	now := time.Now().UTC()
	keys, m := jsonKeys(t, credentialFrom(service.DNSCredential{
		ID: uuid.New(), Name: "n", Provider: "alidns", Zone: "z.example", SecretHint: "****",
		VerifyStatus: service.VerifyUnverified, CreatedAt: now, UpdatedAt: now,
	}))
	expectKeys(t, keys, dnsCredentialKeys)
	if m["verified_at"] != nil || m["verify_error"] != nil || m["visible_zones"] != nil {
		t.Fatalf("nullable %v", m)
	}
}

func TestVerifyResultKeys(t *testing.T) {
	now := time.Now().UTC()
	keys, m := jsonKeys(t, verifyResultFrom(service.VerifyResult{
		Credential: service.DNSCredential{ID: uuid.New(), CreatedAt: now, UpdatedAt: now},
		OK:         false, Message: "", Warnings: nil, Resumed: 0,
	}))
	expectKeys(t, keys, verifyResultKeys)
	warns, _ := m["warnings"].([]any)
	if warns == nil {
		t.Fatalf("warnings 应为 []")
	}
	if m["message"] != "" {
		t.Fatalf("message 应输出空串")
	}
}

func TestACMESettingsKeys(t *testing.T) {
	keys, _ := jsonKeys(t, acmeFrom(service.ACMESettings{}))
	expectKeys(t, keys, acmeSettingsKeys)
}

func TestListCertificatesRoute(t *testing.T) {
	f := &fakeSvc{list: service.CertificateList{
		Items:   []service.Certificate{{ID: uuid.New(), DNSCredentialID: uuid.New(), CreatedAt: time.Now(), UpdatedAt: time.Now()}},
		Summary: service.CertificateSummary{Total: 1},
	}}
	e := newEngine(f)
	w := do(e, http.MethodGet, "/v1/certificates", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
}

func TestRenewAccepted(t *testing.T) {
	cid := uuid.New()
	f := &fakeSvc{renewOut: service.OrderBrief{ID: uuid.New(), State: service.OrderQueued, Reason: service.ReasonManual, CreatedAt: time.Now().UTC()}}
	e := newEngine(f)
	w := do(e, http.MethodPost, "/v1/certificates/"+cid.String()+"/renew", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d", w.Code)
	}
	if f.lastRenewID != cid {
		t.Fatalf("renew id %s", f.lastRenewID)
	}
}

func TestDeleteNoContent(t *testing.T) {
	cid := uuid.New()
	f := &fakeSvc{}
	e := newEngine(f)
	w := do(e, http.MethodDelete, "/v1/certificates/"+cid.String(), "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if f.lastDeleteID != cid {
		t.Fatalf("delete id")
	}
}

func TestCreateCertificateMapping(t *testing.T) {
	cred := uuid.New()
	f := &fakeSvc{}
	e := newEngine(f)
	body := `{"name":"主站","identifiers":["a.example","*.b.example"],"dns_credential_id":"` + cred.String() + `","key_type":"rsa-2048"}`
	w := do(e, http.MethodPost, "/v1/certificates", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.createIn.Name != "主站" || f.createIn.DNSCredentialID != cred.String() || f.createIn.KeyType != "rsa-2048" {
		t.Fatalf("input %+v", f.createIn)
	}
	if len(f.createIn.Identifiers) != 2 {
		t.Fatalf("identifiers %v", f.createIn.Identifiers)
	}
}

func TestPatchCertificateOnlyName(t *testing.T) {
	cid := uuid.New()
	f := &fakeSvc{}
	e := newEngine(f)
	w := do(e, http.MethodPatch, "/v1/certificates/"+cid.String(), `{"name":"新名"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lastPatch.Name == nil || *f.lastPatch.Name != "新名" {
		t.Fatalf("name %+v", f.lastPatch)
	}
	if f.lastPatch.DNSCredentialID != nil || f.lastPatch.KeyType != nil || f.lastPatch.Identifiers != nil {
		t.Fatalf("其余应为 nil %+v", f.lastPatch)
	}
}

func TestCreateCredentialSecretPassthrough(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f)
	w := do(e, http.MethodPost, "/v1/dns-credentials", `{"name":"cf","provider":"cloudflare","zone":"example.com","secret":{"api_token":"tok"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.createCredIn.Secret["api_token"] != "tok" {
		t.Fatalf("secret %v", f.createCredIn.Secret)
	}
}

func TestSaveACMESettingsHMACPointer(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f)
	w := do(e, http.MethodPut, "/v1/settings/acme", `{"contact_email":"a@b.test","use_staging":false,"zerossl_enabled":false,"zerossl_eab_kid":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("omit hmac %d %s", w.Code, w.Body)
	}
	if f.lastACMEIn.ZeroSSLEABHMAC != nil {
		t.Fatalf("缺席应为 nil")
	}

	w = do(e, http.MethodPut, "/v1/settings/acme", `{"contact_email":"a@b.test","use_staging":false,"zerossl_enabled":false,"zerossl_eab_kid":"","zerossl_eab_hmac":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("empty hmac %d %s", w.Code, w.Body)
	}
	if f.lastACMEIn.ZeroSSLEABHMAC == nil || *f.lastACMEIn.ZeroSSLEABHMAC != "" {
		t.Fatalf("空串应为非 nil 指针 %+v", f.lastACMEIn.ZeroSSLEABHMAC)
	}
}

func TestBadUUIDNotFound(t *testing.T) {
	e := newEngine(&fakeSvc{})
	w := do(e, http.MethodGet, "/v1/certificates/not-uuid", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d", w.Code)
	}
}

func TestUnknownJSONFieldBadRequest(t *testing.T) {
	e := newEngine(&fakeSvc{})
	w := do(e, http.MethodPost, "/v1/certificates", `{"name":"x","identifiers":[],"dns_credential_id":"`+uuid.New().String()+`","bogus":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
}

func TestPauseResumeServiceCalls(t *testing.T) {
	cid := uuid.New()
	f := &fakeSvc{}
	e := newEngine(f)
	w := do(e, http.MethodPost, "/v1/certificates/"+cid.String()+"/pause", "")
	if w.Code != http.StatusOK || !f.lastPaused || f.lastPausedID != cid {
		t.Fatalf("pause %d paused=%v id=%s", w.Code, f.lastPaused, f.lastPausedID)
	}
	w = do(e, http.MethodPost, "/v1/certificates/"+cid.String()+"/resume", "")
	if w.Code != http.StatusOK || f.lastPaused || f.lastPausedID != cid {
		t.Fatalf("resume %d paused=%v", w.Code, f.lastPaused)
	}
}
