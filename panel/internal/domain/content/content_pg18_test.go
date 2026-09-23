package content

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const contentPG18Fixture = "disposable-v1"

func TestContentPagesPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_CONTENT_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_CONTENT_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_CONTENT_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_CONTENT_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_CONTENT_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("content PostgreSQL 18 fixture is not configured")
	}
	if fixture != contentPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_content_") ||
		!regexp.MustCompile(`^[a-z0-9]{32}$`).MatchString(runID) {
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

	var database, marker, comment string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_content_test_marker), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`).Scan(&database, &version, &marker, &comment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID || comment != "pandora-content-pg18:"+runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q", database, version, marker, comment)
	}
	var forced bool
	if err := app.QueryRow(ctx, `SELECT bool_and(relrowsecurity AND relforcerowsecurity)
		FROM pg_class WHERE oid IN ('content_pages'::regclass,'audit_events'::regclass,'subscriptions'::regclass)`).Scan(&forced); err != nil || !forced {
		t.Fatalf("content FORCE RLS proof failed forced=%v err=%v", forced, err)
	}

	const (
		tenantA   = "73000000-0000-7000-8000-000000000001"
		tenantB   = "73000000-0000-7000-8000-000000000002"
		actorA    = "73000000-0000-7000-8000-000000000011"
		userA     = "73000000-0000-7000-8000-000000000012"
		outsiderA = "73000000-0000-7000-8000-000000000013"
		userB     = "73000000-0000-7000-8000-000000000014"
		productA  = "73000000-0000-7000-8000-000000000021"
		planA     = "73000000-0000-7000-8000-000000000031"
		planVerA  = "73000000-0000-7000-8000-000000000041"
		subA      = "73000000-0000-7000-8000-000000000051"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'content-pg18-a','Content PG18 A','USD')`, []any{tenantA}},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'content-pg18-b','Content PG18 B','USD')`, []any{tenantB}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'actor@content.invalid','Actor','active')`, []any{tenantA, actorA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'member@content.invalid','Member','active')`, []any{tenantA, userA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'outsider@content.invalid','Outsider','active')`, []any{tenantA, outsiderA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'member-b@content.invalid','Member B','active')`, []any{tenantB, userB}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'content-product','Content Product','active')`, []any{tenantA, productA}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'content-plan','Content Plan','draft')`, []any{tenantA, productA, planA}},
		{`INSERT INTO plan_versions(id,tenant_id,plan_id,version,created_by) VALUES($3,$1,$2,1,$4)`, []any{tenantA, planA, planVerA, actorA}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE tenant_id=$1 AND id=$2`, []any{tenantA, planVerA}},
		{`UPDATE plans SET current_version_id=$2,status='active' WHERE tenant_id=$1 AND id=$3`, []any{tenantA, planVerA, planA}},
		{`INSERT INTO subscriptions(id,tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount)
		  VALUES($4,$1,$2,$3,$5,'active','USD',100)`, []any{tenantA, userA, planA, subA, planVerA}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed content fixture: %v", err)
		}
	}

	svc := New(app)
	base := PublishInput{Slug: "getting-started", Kind: "kb_article", Title: "Getting Started",
		Body: "This is a sufficiently long content body for PostgreSQL acceptance.", Locale: "zh-CN",
		Visibility: "authenticated", TargetPlatforms: []string{"web"},
		MinClientVersion: "1.0.0", MaxClientVersion: "3.0.0", TargetPlanIDs: []string{planA}}
	first, err := svc.PublishVersion(ctx, tenantA, actorA, "content-pg18-create-1", base)
	if err != nil || first.Version != 1 {
		t.Fatalf("publish first version: result=%+v err=%v", first, err)
	}
	base.ExpectedLatestVersion = 1
	base.Status = "published"
	base.Title = "Getting Started v2"
	second, err := svc.PublishVersion(ctx, tenantA, actorA, "content-pg18-create-2", base)
	if err != nil || second.Version != 2 {
		t.Fatalf("publish second version: result=%+v err=%v", second, err)
	}

	visible, err := svc.ListVisible(ctx, tenantA, userA, VisibleFilter{Kind: "kb_article", Platform: "web", ClientVersion: "2.0.0", Locale: "zh-CN"})
	if err != nil || len(visible) != 1 || visible[0].Version != 2 || visible[0].Body != "" {
		t.Fatalf("visible list mismatch pages=%+v err=%v", visible, err)
	}
	detail, err := svc.GetVisible(ctx, tenantA, userA, "getting-started", VisibleFilter{Platform: "web", ClientVersion: "2.0.0", Locale: "zh-CN"})
	if err != nil || detail.Version != 2 || detail.Body == "" {
		t.Fatalf("visible detail mismatch page=%+v err=%v", detail, err)
	}
	for name, tc := range map[string]struct {
		user   string
		filter VisibleFilter
	}{
		"no-plan":  {outsiderA, VisibleFilter{Platform: "web", ClientVersion: "2.0.0", Locale: "zh-CN"}},
		"platform": {userA, VisibleFilter{Platform: "android", ClientVersion: "2.0.0", Locale: "zh-CN"}},
		"version":  {userA, VisibleFilter{Platform: "web", ClientVersion: "4.0.0", Locale: "zh-CN"}},
		"locale":   {userA, VisibleFilter{Platform: "web", ClientVersion: "2.0.0", Locale: "en-US"}},
	} {
		got, err := svc.ListVisible(ctx, tenantA, tc.user, tc.filter)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s filter leaked pages=%+v err=%v", name, got, err)
		}
	}

	countState := func() (pages, audits int) {
		if err := admin.QueryRow(ctx, `SELECT count(*)::int,
			(SELECT count(*)::int FROM audit_events WHERE tenant_id=$1 AND action LIKE 'content.%')
			FROM content_pages WHERE tenant_id=$1`, tenantA).Scan(&pages, &audits); err != nil {
			t.Fatalf("read content state: %v", err)
		}
		return
	}
	beforePages, beforeAudits := countState()
	stale := base
	stale.ExpectedLatestVersion = 1
	stale.Title = "Stale update"
	if _, err := svc.PublishVersion(ctx, tenantA, actorA, "content-pg18-stale", stale); !hasHTTPCode(err, httpx.CodeConflict) {
		t.Fatalf("stale CAS did not conflict: %v", err)
	}
	if pages, audits := countState(); pages != beforePages || audits != beforeAudits {
		t.Fatalf("stale CAS changed state pages=%d/%d audits=%d/%d", pages, beforePages, audits, beforeAudits)
	}

	concurrent := base
	concurrent.Status = "draft"
	concurrent.ExpectedLatestVersion = 2
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := concurrent
			in.Title = fmt.Sprintf("Concurrent %d", i)
			_, err := svc.PublishVersion(ctx, tenantA, actorA, fmt.Sprintf("content-pg18-race-%d", i), in)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	successes, conflicts := 0, 0
	for err := range errCh {
		if err == nil {
			successes++
		} else if hasHTTPCode(err, httpx.CodeConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS successes=%d conflicts=%d", successes, conflicts)
	}

	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantB, ActorID: userB}, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM content_pages WHERE tenant_id=$1`, tenantA).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("cross-tenant RLS exposed %d rows", count)
		}
		return nil
	}); err != nil {
		t.Fatalf("prove content RLS: %v", err)
	}

	beforePages, beforeAudits = countState()
	if _, err := admin.Exec(ctx, `REVOKE INSERT ON audit_events FROM aegis_app`); err != nil {
		t.Fatalf("inject audit fault: %v", err)
	}
	fault := concurrent
	fault.ExpectedLatestVersion = 3
	fault.Title = "Audit must roll back"
	_, faultErr := svc.PublishVersion(ctx, tenantA, actorA, "content-pg18-audit-fault", fault)
	if _, err := admin.Exec(ctx, `GRANT INSERT ON audit_events TO aegis_app`); err != nil {
		t.Fatalf("restore audit grant: %v", err)
	}
	if faultErr == nil {
		t.Fatal("audit fault unexpectedly committed content")
	}
	if pages, audits := countState(); pages != beforePages || audits != beforeAudits {
		t.Fatalf("audit fault changed state pages=%d/%d audits=%d/%d", pages, beforePages, audits, beforeAudits)
	}

	adminRows, err := svc.ListAdmin(ctx, tenantB, userB, ListFilter{})
	if err != nil || len(adminRows) != 0 {
		t.Fatalf("cross-tenant admin list leaked rows=%+v err=%v", adminRows, err)
	}
	t.Logf("content_pg18_business=ok role=aegis_app rls=on schema=40 pages=%d audits=%d concurrent=1/1 filters=plan,platform,version,locale audit_rollback=ok", beforePages, beforeAudits)
}

func hasHTTPCode(err error, code httpx.Code) bool {
	var apiErr *httpx.Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}
