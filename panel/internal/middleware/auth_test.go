package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

const (
	authTestTenant  = "11111111-1111-4111-8111-111111111111"
	authTestUser    = "22222222-2222-4222-8222-222222222222"
	authTestSession = "33333333-3333-4333-8333-333333333333"
)

func TestSessionValidityQueryFailsClosedAcrossPrincipalBoundaries(t *testing.T) {
	for _, clause := range []string{
		"revoked_at IS NOT NULL",
		"expires_at <= now()",
		"tenant_id = $1",
		"id = $2::uuid",
		"user_id = $3::uuid",
		"audience = $4",
	} {
		if !strings.Contains(sessionValiditySQL, clause) {
			t.Fatalf("session validity query is missing %q: %s", clause, sessionValiditySQL)
		}
	}
}

type authFakeRunner struct {
	tx    *authFakeTx
	calls int
}

func (r *authFakeRunner) InTx(ctx context.Context, _ db.Scope, fn func(pgx.Tx) error) error {
	r.calls++
	return fn(r.tx)
}

type authFakeTx struct {
	pgx.Tx
	revoked         bool
	sessionErr      error
	permissionQuery string
	permissionArgs  []any
}

func (tx *authFakeTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return authFakeRow{revoked: tx.revoked, err: tx.sessionErr}
}

func (tx *authFakeTx) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.permissionQuery = query
	tx.permissionArgs = args
	return &authEmptyRows{}, nil
}

type authEmptyRows struct{}

func (*authEmptyRows) Close()                                       {}
func (*authEmptyRows) Err() error                                   { return nil }
func (*authEmptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*authEmptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*authEmptyRows) Next() bool                                   { return false }
func (*authEmptyRows) Scan(...any) error                            { return nil }
func (*authEmptyRows) Values() ([]any, error)                       { return nil, nil }
func (*authEmptyRows) RawValues() [][]byte                          { return nil }
func (*authEmptyRows) Conn() *pgx.Conn                              { return nil }

type authFakeRow struct {
	revoked bool
	err     error
}

func (r authFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*bool)) = r.revoked
	return nil
}

func TestAuthenticateRejectsRevokedSessionToken(t *testing.T) {
	runner := &authFakeRunner{tx: &authFakeTx{revoked: true}}
	issuer := token.NewIssuer("public", []byte("01234567890123456789012345678901"), 30*24*time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, SessionID: authTestSession,
		Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	nextCalled := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Authenticate(runner, issuer, log)(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req = req.WithContext(httpx.WithTenantID(req.Context(), authTestTenant))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", res.Code, res.Body.String())
	}
	if nextCalled {
		t.Fatal("revoked token reached protected handler")
	}
	if runner.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", runner.calls)
	}
}

func TestAuthenticateRejectsSignedTokenWithoutSessionBeforeDatabase(t *testing.T) {
	runner := &authFakeRunner{tx: &authFakeTx{}}
	issuer := token.NewIssuer("admin", []byte("01234567890123456789012345678901"), 30*24*time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Authenticate(runner, issuer, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("sessionless token reached protected handler")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req = req.WithContext(httpx.WithTenantID(req.Context(), authTestTenant))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", res.Code, res.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("database transaction calls = %d, want 0", runner.calls)
	}
}

func TestAuthenticateRejectsCrossTenantClaimsBeforeDatabase(t *testing.T) {
	runner := &authFakeRunner{tx: &authFakeTx{}}
	issuer := token.NewIssuer("public", []byte("01234567890123456789012345678901"), time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, SessionID: authTestSession, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Authenticate(runner, issuer, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-tenant token reached protected handler")
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req = req.WithContext(httpx.WithTenantID(req.Context(), "44444444-4444-4444-8444-444444444444"))
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized || runner.calls != 0 {
		t.Fatalf("cross-tenant result status=%d db_calls=%d", res.Code, runner.calls)
	}
}

func TestAuthenticateAcceptsActiveTenantBoundSession(t *testing.T) {
	runner := &authFakeRunner{tx: &authFakeTx{revoked: false}}
	issuer := token.NewIssuer("public", []byte("01234567890123456789012345678901"), time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, SessionID: authTestSession, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nextCalled := false
	handler := Authenticate(runner, issuer, log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		principal := httpx.PrincipalFrom(r.Context())
		if principal.UserID != authTestUser || principal.SessionID != authTestSession || principal.TenantID != authTestTenant {
			t.Fatalf("unexpected principal: %+v", principal)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req = req.WithContext(httpx.WithTenantID(req.Context(), authTestTenant))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if !nextCalled || res.Code != http.StatusOK || runner.calls != 1 {
		t.Fatalf("active session result next=%v status=%d db_calls=%d", nextCalled, res.Code, runner.calls)
	}
}

func TestAdminPermissionExpansionRequiresTenantScope(t *testing.T) {
	tx := &authFakeTx{}
	runner := &authFakeRunner{tx: tx}
	issuer := token.NewIssuer("admin", []byte("01234567890123456789012345678901"), time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, SessionID: authTestSession, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := Authenticate(runner, issuer, slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req = req.WithContext(httpx.WithTenantID(req.Context(), authTestTenant))
	handler.ServeHTTP(httptest.NewRecorder(), req)
	for _, want := range []string{"JOIN roles r", "scope_type = 'tenant'", "scope_id IS NULL", "$3::text <> 'admin'"} {
		if !strings.Contains(tx.permissionQuery, want) {
			t.Fatalf("admin permission query missing %q: %s", want, tx.permissionQuery)
		}
	}
	if len(tx.permissionArgs) != 3 || tx.permissionArgs[2] != "admin" {
		t.Fatalf("permission args=%#v, want admin audience", tx.permissionArgs)
	}
}
