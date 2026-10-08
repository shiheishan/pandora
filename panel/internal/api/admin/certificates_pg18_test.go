package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/certs"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// 证书后台接口对真库（certs 域，与 domain/certs 的 PG18 用例同库）：读接口 JSON 里没有任何密文字段
// 与令牌明文；PATCH 不带令牌保留原值；ACME 设置的 EAB HMAC 只回「配没配」。Cloudflare 用进程内模拟。
func TestCertificatesAdminPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "CERTS", DatabasePrefix: "pandora_certs_", MarkerTable: "pandora_certs_test_marker",
		CommentTag: "pandora-certs-pg18",
	})
	const (
		tenant = "ce570000-0000-4000-8000-000000000101"
		actor  = "ce570000-0000-4000-8000-000000000111"
		token  = "cf-admin-test-token-7788"
		hmac   = "YWRtaW4tdGVzdC1obWFj"
	)
	step3Seed(t, ctx, admin,
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('`+tenant+`','certs-admin-pg18','Certs','CNY')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('`+actor+`','`+tenant+`','ops@certs-admin.invalid','Ops','active')`)

	env, err := crypto.NewEnvelope([]byte("certs-admin-pg18-envelope-key-32"))
	if err != nil {
		t.Fatal(err)
	}
	cf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`)
			return
		}
		switch {
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"z1","name":"example.com"}],"result_info":{"total_count":1}}`)
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"r1"}}`)
		default:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"r1"}}`)
		}
	})
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		cf.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
	h := &certHandlers{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		svc: certs.NewService(app, env, certs.Options{CloudflareBaseURL: "https://cf.test/client/v4", ProviderHTTPClient: client})}
	r := step5Router(tenant, actor, nil, func(r chi.Router) {
		r.Get("/v1/dns-credentials", h.listCredentials)
		r.Post("/v1/dns-credentials", h.createCredential)
		r.Patch("/v1/dns-credentials/{id}", h.updateCredential)
		r.Get("/v1/certificates", h.list)
		r.Post("/v1/certificates", h.create)
		r.Get("/v1/certificates/{id}", h.detail)
		r.Get("/v1/settings/acme", h.acmeSettings)
		r.Put("/v1/settings/acme", h.saveACMESettings)
	})
	noSecrets := func(label, body string) {
		t.Helper()
		for _, banned := range []string{"sealed", token, hmac, "api_token", "private_key"} {
			if strings.Contains(body, banned) {
				t.Fatalf("%s response leaks %q: %s", label, banned, body)
			}
		}
	}

	w := step3Do(t, ctx, r, http.MethodPost, "/v1/dns-credentials",
		`{"name":"cf","provider":"cloudflare","zone":"example.com","secret":{"api_token":"`+token+`"}}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("create credential: %d %s", w.Code, w.Body.String())
	}
	noSecrets("create credential", w.Body.String())
	var created struct {
		Credential struct {
			ID         string `json:"id"`
			SecretHint string `json:"secret_hint"`
		} `json:"credential"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Credential.SecretHint != "7788" {
		t.Fatalf("created = %+v err=%v", created, err)
	}
	id := created.Credential.ID

	// 只改名：令牌不动，校验状态不动
	w = step3Do(t, ctx, r, http.MethodPatch, "/v1/dns-credentials/"+id, `{"name":"cf-main"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"verify_status":"ok"`) {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	noSecrets("patch credential", w.Body.String())
	var stillOK bool
	if err := admin.QueryRow(ctx, `SELECT verify_status = 'ok' AND secret_hint = '7788' FROM dns_credentials WHERE id = $1`, id).
		Scan(&stillOK); err != nil || !stillOK {
		t.Fatalf("token must survive a PATCH without it: ok=%v err=%v", stillOK, err)
	}
	// 未知字段（拼错的键）被拒
	if w = step3Do(t, ctx, r, http.MethodPatch, "/v1/dns-credentials/"+id, `{"token":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", w.Code, w.Body.String())
	}

	w = step3Do(t, ctx, r, http.MethodPost, "/v1/certificates",
		`{"name":"wild","identifiers":["*.example.com","example.com"],"dns_credential_id":"`+id+`"}`)
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"wildcard":true`) ||
		!strings.Contains(w.Body.String(), `"state":"queued"`) {
		t.Fatalf("create certificate: %d %s", w.Code, w.Body.String())
	}
	if w = step3Do(t, ctx, r, http.MethodPost, "/v1/certificates",
		`{"name":"ip","identifiers":["192.0.2.10"],"dns_credential_id":"`+id+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("IP identifiers must be rejected: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/v1/certificates", "/v1/dns-credentials"} {
		w = step3Do(t, ctx, r, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		noSecrets(path, w.Body.String())
	}

	w = step3Do(t, ctx, r, http.MethodPut, "/v1/settings/acme",
		`{"contact_email":"ops@example.com","use_staging":false,"zerossl_enabled":true,"zerossl_eab_kid":"kid-1","zerossl_eab_hmac":"`+hmac+`"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"zerossl_eab_hmac_set":true`) {
		t.Fatalf("save acme settings: %d %s", w.Code, w.Body.String())
	}
	noSecrets("save acme settings", w.Body.String())
	w = step3Do(t, ctx, r, http.MethodPut, "/v1/settings/acme",
		`{"contact_email":"ops@example.com","use_staging":true,"zerossl_enabled":true,"zerossl_eab_kid":"kid-1"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"zerossl_eab_hmac_set":true`) {
		t.Fatalf("HMAC must survive a save without it: %d %s", w.Code, w.Body.String())
	}
	w = step3Do(t, ctx, r, http.MethodGet, "/v1/settings/acme", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"use_staging":true`) {
		t.Fatalf("get acme settings: %d %s", w.Code, w.Body.String())
	}
	noSecrets("get acme settings", w.Body.String())
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
