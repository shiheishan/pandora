package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	testTenantID  = "11111111-1111-4111-8111-111111111111"
	testUserID    = "22222222-2222-4222-8222-222222222222"
	testSessionID = "33333333-3333-4333-8333-333333333333"
)

type recordedExec struct {
	query string
	args  []any
}

type logoutFakeTx struct {
	pgx.Tx
	sessionRows        int64
	exactSessionExists bool
	execs              []recordedExec
}

func (tx *logoutFakeTx) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, recordedExec{query: query, args: args})
	switch {
	case strings.Contains(query, "UPDATE sessions"):
		return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", tx.sessionRows)), nil
	case strings.Contains(query, "UPDATE refresh_tokens"):
		return pgconn.NewCommandTag("UPDATE 2"), nil
	case strings.Contains(query, "pg_advisory_xact_lock"):
		return pgconn.NewCommandTag("SELECT 1"), nil
	case strings.Contains(query, "INSERT INTO audit_events"):
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	default:
		return pgconn.CommandTag{}, errors.New("unexpected Exec query")
	}
}

func (tx *logoutFakeTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "SELECT EXISTS") && strings.Contains(query, "FROM sessions") {
		return logoutFakeRow{value: tx.exactSessionExists}
	}
	if !strings.Contains(query, "SELECT entry_hash FROM audit_events") {
		return logoutFakeRow{err: errors.New("unexpected QueryRow query")}
	}
	return logoutFakeRow{err: pgx.ErrNoRows}
}

type logoutFakeRow struct {
	value any
	err   error
}

func (r logoutFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 1 {
		return errors.New("unexpected scan destination count")
	}
	switch out := dest[0].(type) {
	case *bool:
		*out = r.value.(bool)
		return nil
	default:
		return errors.New("unexpected scan destination")
	}
}

type logoutFakeRunner struct {
	tx    *logoutFakeTx
	scope db.Scope
	calls int
}

func (r *logoutFakeRunner) InTx(ctx context.Context, scope db.Scope, fn func(pgx.Tx) error) error {
	r.calls++
	r.scope = scope
	return fn(r.tx)
}

func TestLogoutCurrentSessionRevokesExactSessionAndRefreshChain(t *testing.T) {
	for _, tt := range []struct {
		audience, actorKind string
	}{
		{audience: "public", actorKind: "user"},
		{audience: "admin", actorKind: "admin"},
	} {
		t.Run(tt.audience, func(t *testing.T) {
			tx := &logoutFakeTx{sessionRows: 1, exactSessionExists: true}
			runner := &logoutFakeRunner{tx: tx}
			svc := &Service{pool: runner}

			err := svc.LogoutCurrentSession(context.Background(), testTenantID, LogoutInput{
				UserID: testUserID, SessionID: testSessionID, Audience: tt.audience,
			})
			if err != nil {
				t.Fatalf("LogoutCurrentSession returned error: %v", err)
			}
			if runner.calls != 1 {
				t.Fatalf("transaction calls = %d, want 1", runner.calls)
			}
			if runner.scope.TenantID != testTenantID || runner.scope.ActorID != testUserID {
				t.Fatalf("unexpected transaction scope: %+v", runner.scope)
			}

			sessionUpdate := findRecordedExec(t, tx.execs, "UPDATE sessions")
			for _, clause := range []string{"tenant_id = $1", "id = $2::uuid", "user_id = $3::uuid", "audience = $4"} {
				if !strings.Contains(sessionUpdate.query, clause) {
					t.Fatalf("session revocation is missing scope clause %q: %s", clause, sessionUpdate.query)
				}
			}
			assertArgs(t, sessionUpdate.args, testTenantID, testSessionID, testUserID, tt.audience)

			refreshUpdate := findRecordedExec(t, tx.execs, "UPDATE refresh_tokens")
			if !strings.Contains(refreshUpdate.query, "session_id = $2::uuid") ||
				!strings.Contains(refreshUpdate.query, "status IN ('active', 'rotated')") {
				t.Fatalf("refresh-token chain is not fully session-scoped: %s", refreshUpdate.query)
			}
			assertArgs(t, refreshUpdate.args, testTenantID, testSessionID, testUserID)

			auditInsert := findRecordedExec(t, tx.execs, "INSERT INTO audit_events")
			if got := auditInsert.args[1]; got != tt.actorKind {
				t.Fatalf("audit actor kind = %v, want %q", got, tt.actorKind)
			}
			if got := auditInsert.args[4]; got != "user.logout" {
				t.Fatalf("audit action = %v, want user.logout", got)
			}
		})
	}
}

func TestLogoutCurrentSessionRejectsMismatchedSession(t *testing.T) {
	tx := &logoutFakeTx{sessionRows: 0, exactSessionExists: false}
	runner := &logoutFakeRunner{tx: tx}
	svc := &Service{pool: runner}

	err := svc.LogoutCurrentSession(context.Background(), testTenantID, LogoutInput{
		UserID: testUserID, SessionID: testSessionID, Audience: "public",
	})
	assertHTTPErrorCode(t, err, httpx.CodeUnauthorized)
	if runner.calls != 1 {
		t.Fatalf("transaction calls = %d, want 1", runner.calls)
	}
	for _, call := range tx.execs {
		if strings.Contains(call.query, "UPDATE refresh_tokens") || strings.Contains(call.query, "audit_events") {
			t.Fatalf("mismatched session must not revoke refresh tokens or write success audit: %s", call.query)
		}
	}
}

func TestLogoutCurrentSessionConcurrentDuplicateIsTerminalSuccessWithoutDuplicateAudit(t *testing.T) {
	tx := &logoutFakeTx{sessionRows: 0, exactSessionExists: true}
	runner := &logoutFakeRunner{tx: tx}
	svc := &Service{pool: runner}

	err := svc.LogoutCurrentSession(context.Background(), testTenantID, LogoutInput{
		UserID: testUserID, SessionID: testSessionID, Audience: "public",
	})
	if err != nil {
		t.Fatalf("duplicate terminal logout returned error: %v", err)
	}
	for _, call := range tx.execs {
		if strings.Contains(call.query, "UPDATE refresh_tokens") || strings.Contains(call.query, "audit_events") {
			t.Fatalf("duplicate terminal logout must not repeat side effects: %s", call.query)
		}
	}
}

func TestLogoutCurrentSessionRejectsInvalidPrincipalBeforeTransaction(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		input    LogoutInput
	}{
		{"missing session", testTenantID, LogoutInput{UserID: testUserID, Audience: "public"}},
		{"invalid user", testTenantID, LogoutInput{UserID: "not-a-uuid", SessionID: testSessionID, Audience: "public"}},
		{"invalid tenant", "not-a-uuid", LogoutInput{UserID: testUserID, SessionID: testSessionID, Audience: "public"}},
		{"invalid audience", testTenantID, LogoutInput{UserID: testUserID, SessionID: testSessionID, Audience: "node"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &logoutFakeRunner{tx: &logoutFakeTx{sessionRows: 1}}
			svc := &Service{pool: runner}
			err := svc.LogoutCurrentSession(context.Background(), tt.tenantID, tt.input)
			assertHTTPErrorCode(t, err, httpx.CodeUnauthorized)
			if runner.calls != 0 {
				t.Fatalf("transaction calls = %d, want 0", runner.calls)
			}
		})
	}
}

func findRecordedExec(t *testing.T, calls []recordedExec, needle string) recordedExec {
	t.Helper()
	for _, call := range calls {
		if strings.Contains(call.query, needle) {
			return call
		}
	}
	t.Fatalf("no Exec call contains %q", needle)
	return recordedExec{}
}

func assertArgs(t *testing.T, got []any, want ...any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func assertHTTPErrorCode(t *testing.T, err error, want httpx.Code) {
	t.Helper()
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) {
		t.Fatalf("error %v is not *httpx.Error", err)
	}
	if httpErr.Code != want {
		t.Fatalf("error code = %q, want %q", httpErr.Code, want)
	}
}
