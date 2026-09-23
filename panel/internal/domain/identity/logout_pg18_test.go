package identity

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/token"
)

const logoutPG18Fixture = "disposable-v1"

func TestLogoutCurrentSessionPG18ConcurrentSingleTransition(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_LOGOUT_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("logout PostgreSQL 18 fixture is not configured")
	}
	if fixture != logoutPG18Fixture || appDSN == "" || adminDSN == "" ||
		expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_logout_") ||
		!regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{15,80}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator pool: %v", err)
	}
	defer admin.Close()
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open real aegis_app pool: %v", err)
	}
	defer app.Close()

	var database, marker, systemIdentifier, databaseOID, databaseComment string
	var version int
	if err := admin.QueryRow(ctx, `
		SELECT current_database(), current_setting('server_version_num')::int,
		       (SELECT run_id FROM public.pandora_logout_test_marker),
		       (SELECT system_identifier FROM public.pandora_logout_test_marker),
		       (SELECT database_oid::text FROM public.pandora_logout_test_marker),
		       shobj_description(oid, 'pg_database')
		  FROM pg_database WHERE datname=current_database()`).Scan(
		&database, &version, &marker, &systemIdentifier, &databaseOID, &databaseComment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	wantComment := "pandora-logout-pg18:" + runID
	if database != expectedDatabase || version/10000 != 18 || marker != runID ||
		systemIdentifier == "" || databaseOID == "" || databaseComment != wantComment {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q system=%q oid=%q comment=%q", database, version, marker, systemIdentifier, databaseOID, databaseComment)
	}

	// The application connection performs every business write below. Prove it
	// targets the exact same one-time disposable database before seeding any
	// rows or acquiring the control lock; validating only the administrator DSN
	// would still permit a miswired App DSN to mutate another database.
	var appDatabase, appMarker, appSystemIdentifier, appDatabaseOID, appDatabaseComment string
	var appVersion int
	if err := app.QueryRow(ctx, `
		SELECT current_database(), current_setting('server_version_num')::int,
		       (SELECT run_id FROM public.pandora_logout_test_marker),
		       (SELECT system_identifier FROM public.pandora_logout_test_marker),
		       (SELECT database_oid::text FROM public.pandora_logout_test_marker),
		       shobj_description(oid, 'pg_database')
		  FROM pg_database WHERE datname=current_database()`).Scan(
		&appDatabase, &appVersion, &appMarker, &appSystemIdentifier,
		&appDatabaseOID, &appDatabaseComment); err != nil {
		t.Fatalf("identify application PostgreSQL fixture: %v", err)
	}
	if appDatabase != expectedDatabase || appVersion != version || appMarker != runID ||
		appSystemIdentifier != systemIdentifier || appDatabaseOID != databaseOID ||
		appDatabaseComment != wantComment {
		t.Fatalf("refusing mismatched application fixture database=%q version=%d marker=%q system=%q oid=%q comment=%q",
			appDatabase, appVersion, appMarker, appSystemIdentifier, appDatabaseOID, appDatabaseComment)
	}

	const (
		tenantID  = "71000000-0000-7000-8000-000000000001"
		userID    = "71000000-0000-7000-8000-000000000011"
		sessionID = "71000000-0000-7000-8000-000000000021"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'logout-pg18', 'Logout PG18', 'USD')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'logout-pg18@example.test', 'Logout PG18', 'active')`, []any{tenantID, userID}},
		{`INSERT INTO sessions (id, tenant_id, user_id, audience, auth_methods, expires_at) VALUES ($3, $1, $2, 'public', ARRAY['password'], now() + interval '30 days')`, []any{tenantID, userID, sessionID}},
		{`INSERT INTO refresh_tokens (tenant_id, session_id, user_id, token_hash, status, expires_at)
		  VALUES ($1, $3, $2, decode('01', 'hex'), 'active', now() + interval '30 days'),
		         ($1, $3, $2, decode('02', 'hex'), 'rotated', now() + interval '30 days'),
		         ($1, $3, $2, decode('03', 'hex'), 'compromised', now() + interval '30 days')`, []any{tenantID, userID, sessionID}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed logout fixture: %v", err)
		}
	}

	issuer := token.NewIssuer("public", []byte("logout-pg18-test-secret-32bytes!"), 30*24*time.Hour)
	oldToken, err := issuer.Issue(token.Claims{
		Subject: userID, TenantID: tenantID, SessionID: sessionID, Kind: "user",
	})
	if err != nil {
		t.Fatalf("issue old access token: %v", err)
	}

	control, err := admin.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin control lock: %v", err)
	}
	controlReleased := false
	defer func() {
		if !controlReleased {
			_ = control.Rollback(context.Background())
		}
	}()
	if err := control.QueryRow(ctx, `SELECT true FROM sessions WHERE id=$1 FOR UPDATE`, sessionID).Scan(new(bool)); err != nil {
		t.Fatalf("lock target session: %v", err)
	}

	svc := &Service{pool: app}
	const workers = 20
	ready := make(chan struct{}, workers)
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			err := svc.LogoutCurrentSession(ctx, tenantID, LogoutInput{
				UserID: userID, SessionID: sessionID, Audience: "public",
			})
			if err != nil {
				errs <- fmt.Errorf("worker %d: %w", worker, err)
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-ready
	}
	close(start)

	peakWaiters := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiters int
		if err := admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			 WHERE datname=$1 AND usename='aegis_app'
			   AND wait_event_type='Lock' AND query LIKE '%UPDATE sessions%'`, expectedDatabase).Scan(&waiters); err != nil {
			t.Fatalf("observe logout lock waiters: %v", err)
		}
		if waiters > peakWaiters {
			peakWaiters = waiters
		}
		if peakWaiters >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if peakWaiters < 2 {
		t.Fatalf("database contention was not proven: peak waiters=%d", peakWaiters)
	}
	if err := control.Commit(ctx); err != nil {
		t.Fatalf("release control lock: %v", err)
	}
	controlReleased = true
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent logout failed: %v", err)
		}
	}
	if t.Failed() {
		return
	}

	var revoked bool
	var refreshTotal, refreshRevoked, refreshCompromised, audits int
	var auditExact bool
	if err := admin.QueryRow(ctx, `SELECT revoked_at IS NOT NULL AND revoked_reason='user_logout' FROM sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3 AND audience='public'`, tenantID, userID, sessionID).Scan(&revoked); err != nil {
		t.Fatalf("read session terminal state: %v", err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='revoked'), count(*) FILTER (WHERE status='compromised') FROM refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND session_id=$3`, tenantID, userID, sessionID).Scan(&refreshTotal, &refreshRevoked, &refreshCompromised); err != nil {
		t.Fatalf("read refresh terminal states: %v", err)
	}
	if err := admin.QueryRow(ctx, `
		SELECT count(*), COALESCE(bool_and(actor_kind='user' AND actor_id=$2 AND api_domain='public' AND outcome='success'), false)
		  FROM audit_events WHERE tenant_id=$1 AND action='user.logout' AND resource_type='session' AND resource_id=$3`,
		tenantID, userID, sessionID).Scan(&audits, &auditExact); err != nil {
		t.Fatalf("read logout audit count: %v", err)
	}
	if !revoked || refreshTotal != 3 || refreshRevoked != 2 || refreshCompromised != 1 || audits != 1 || !auditExact {
		t.Fatalf("terminal state revoked=%v refresh=%d/%d/%d audits=%d exact=%v", revoked, refreshTotal, refreshRevoked, refreshCompromised, audits, auditExact)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nextCalled := false
	handler := middleware.Authenticate(app, issuer, log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	req = req.WithContext(httpx.WithTenantID(req.Context(), tenantID))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if nextCalled || res.Code != http.StatusUnauthorized ||
		!strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") ||
		res.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(res.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("old token response next=%v status=%d headers=%v body=%s", nextCalled, res.Code, res.Header(), res.Body.String())
	}
	t.Logf("PG18 concurrent logout workers=%d peak_waiters=%d audit_events=%d old_token_status=%d", workers, peakWaiters, audits, res.Code)
}
