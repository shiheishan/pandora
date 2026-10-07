package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

const (
	authTestTenant  = "11111111-1111-4111-8111-111111111111"
	authTestUser    = "22222222-2222-4222-8222-222222222222"
	authTestSession = "33333333-3333-4333-8333-333333333333"
)

type authFakeQuerier struct {
	revoked     bool
	permissions []string
	err         error
	calls       int
	scope       db.Scope
	sql         string
	args        []any
}

func (q *authFakeQuerier) QueryRowScoped(_ context.Context, s db.Scope, sql string, args []any, dest ...any) error {
	q.calls++
	q.scope, q.sql, q.args = s, sql, args
	if q.err != nil {
		return q.err
	}
	*(dest[0].(*bool)) = q.revoked
	*(dest[1].(*[]string)) = q.permissions
	return nil
}

func authRequest(t *testing.T, audience, tenant string) *http.Request {
	t.Helper()
	issuer := token.NewIssuer(audience, []byte("01234567890123456789012345678901"), time.Hour)
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, SessionID: authTestSession, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	return req.WithContext(httpx.WithTenantID(req.Context(), tenant))
}

func authIssuer(audience string) *token.Issuer {
	return token.NewIssuer(audience, []byte("01234567890123456789012345678901"), time.Hour)
}

func TestAuthenticateRejectsRevokedSessionToken(t *testing.T) {
	q := &authFakeQuerier{revoked: true}
	nextCalled := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res := httptest.NewRecorder()
	Authenticate(q, authIssuer("public"), log)(next).ServeHTTP(res, authRequest(t, "public", authTestTenant))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", res.Code, res.Body.String())
	}
	if nextCalled {
		t.Fatal("revoked token reached protected handler")
	}
	if q.calls != 1 {
		t.Fatalf("database round trips = %d, want 1", q.calls)
	}
}

// 会话行不存在（被删、属于别的用户或别的 audience）与已吊销同样拒绝。
func TestAuthenticateRejectsMissingSessionRow(t *testing.T) {
	q := &authFakeQuerier{err: pgx.ErrNoRows}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res := httptest.NewRecorder()
	Authenticate(q, authIssuer("public"), log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("token without a session row reached protected handler")
	})).ServeHTTP(res, authRequest(t, "public", authTestTenant))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}
}

func TestAuthenticateFailsClosedOnDatabaseError(t *testing.T) {
	q := &authFakeQuerier{err: errors.New("connection reset")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res := httptest.NewRecorder()
	Authenticate(q, authIssuer("admin"), log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request reached handler without a verified session")
	})).ServeHTTP(res, authRequest(t, "admin", authTestTenant))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
}

func TestAuthenticateRejectsSignedTokenWithoutSessionBeforeDatabase(t *testing.T) {
	q := &authFakeQuerier{}
	issuer := authIssuer("admin")
	raw, err := issuer.Issue(token.Claims{
		Subject: authTestUser, TenantID: authTestTenant, Kind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Authenticate(q, issuer, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
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
	if q.calls != 0 {
		t.Fatalf("database calls = %d, want 0", q.calls)
	}
}

func TestAuthenticateRejectsCrossTenantClaimsBeforeDatabase(t *testing.T) {
	q := &authFakeQuerier{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	res := httptest.NewRecorder()
	Authenticate(q, authIssuer("public"), log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-tenant token reached protected handler")
	})).ServeHTTP(res, authRequest(t, "public", "44444444-4444-4444-8444-444444444444"))
	if res.Code != http.StatusUnauthorized || q.calls != 0 {
		t.Fatalf("cross-tenant result status=%d db_calls=%d", res.Code, q.calls)
	}
}

func TestAuthenticateAcceptsActiveTenantBoundSession(t *testing.T) {
	q := &authFakeQuerier{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nextCalled := false
	handler := Authenticate(q, authIssuer("public"), log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		principal := httpx.PrincipalFrom(r.Context())
		if principal.UserID != authTestUser || principal.SessionID != authTestSession || principal.TenantID != authTestTenant {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		if principal.Permissions != nil {
			t.Fatalf("portal principal carries permissions: %v", principal.Permissions)
		}
	}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, authRequest(t, "public", authTestTenant))
	if !nextCalled || res.Code != http.StatusOK || q.calls != 1 {
		t.Fatalf("active session result next=%v status=%d db_calls=%d", nextCalled, res.Code, q.calls)
	}
	// 有效性、节流刷新（R62）与权限展开在同一条语句、同一次往返里
	// 语句本身的断言在 platform/sessionauth 的测试里
	if !strings.Contains(q.sql, "UPDATE sessions SET last_seen_at") ||
		q.scope != (db.Scope{TenantID: authTestTenant, ActorID: authTestUser}) {
		t.Fatalf("auth query scope=%+v sql=%q", q.scope, q.sql)
	}
	if len(q.args) != 4 || q.args[0] != authTestTenant || q.args[1] != authTestSession ||
		q.args[2] != authTestUser || q.args[3] != "public" {
		t.Fatalf("auth query args=%#v", q.args)
	}
}

func TestAdminPermissionExpansionRequiresTenantScope(t *testing.T) {
	q := &authFakeQuerier{permissions: []string{"iam.user.read", "node.read"}}
	var got []string
	handler := Authenticate(q, authIssuer("admin"), slog.New(slog.NewTextHandler(io.Discard, nil)))(
		http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got = httpx.PrincipalFrom(r.Context()).Permissions
		}))
	handler.ServeHTTP(httptest.NewRecorder(), authRequest(t, "admin", authTestTenant))
	if len(q.args) != 4 || q.args[3] != "admin" {
		t.Fatalf("permission args=%#v, want admin audience", q.args)
	}
	if strings.Join(got, ",") != "iam.user.read,node.read" {
		t.Fatalf("admin principal permissions = %v", got)
	}
}
