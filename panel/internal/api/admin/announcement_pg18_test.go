package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/notify"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const announcementPG18Fixture = "disposable-v1"

func TestAnnouncementPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_ANNOUNCEMENT_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_ANNOUNCEMENT_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_ANNOUNCEMENT_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_ANNOUNCEMENT_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_ANNOUNCEMENT_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("announcement PostgreSQL 18 fixture is not configured")
	}
	if fixture != announcementPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_node_preview_") ||
		!regexp.MustCompile(`^[a-z0-9]{32}$`).MatchString(runID) {
		t.Fatalf("refusing malformed disposable identity database=%q run_id=%q", expectedDatabase, runID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	adminDB, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("open fixture administrator pool: %v", err)
	}
	defer adminDB.Close()
	app, err := platformdb.Open(ctx, appDSN)
	if err != nil {
		t.Fatalf("open real aegis_app pool: %v", err)
	}
	defer app.Close()

	var database, marker, comment string
	var version int
	if err := app.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int,
		(SELECT run_id FROM public.pandora_announcement_test_marker), shobj_description(oid,'pg_database')
		FROM pg_database WHERE datname=current_database()`).Scan(&database, &version, &marker, &comment); err != nil {
		t.Fatalf("identify PostgreSQL fixture: %v", err)
	}
	if database != expectedDatabase || version/10000 != 18 || marker != runID || comment != "pandora-node-preview-pg18:"+runID {
		t.Fatalf("refusing unexpected fixture database=%q version=%d marker=%q comment=%q", database, version, marker, comment)
	}

	const (
		tenantA  = "75000000-0000-4000-8000-000000000001"
		tenantB  = "75000000-0000-4000-8000-000000000002"
		actorA   = "75000000-0000-4000-8000-000000000011"
		actorB   = "75000000-0000-4000-8000-000000000012"
		productA = "75000000-0000-4000-8000-000000000021"
		productB = "75000000-0000-4000-8000-000000000022"
		planA    = "75000000-0000-4000-8000-000000000031"
		planB    = "75000000-0000-4000-8000-000000000032"
		dueID    = "75000000-0000-4000-8000-000000000041"
	)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'announcement-a','Announcement A','USD')`, []any{tenantA}},
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'announcement-b','Announcement B','USD')`, []any{tenantB}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'actor-a@announcement.invalid','Actor A','active')`, []any{tenantA, actorA}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'actor-b@announcement.invalid','Actor B','active')`, []any{tenantB, actorB}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'announcement-product-a','Product A','active')`, []any{tenantA, productA}},
		{`INSERT INTO products(id,tenant_id,code,name,status) VALUES($2,$1,'announcement-product-b','Product B','active')`, []any{tenantB, productB}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'announcement-plan-a','Plan A','draft')`, []any{tenantA, productA, planA}},
		{`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES($3,$1,$2,'announcement-plan-b','Plan B','draft')`, []any{tenantB, productB, planB}},
		{`INSERT INTO announcements(id,tenant_id,content,severity,status,publish_at,version,created_by)
		  VALUES($2,$1,'{"title":"Due","body":"Scheduled body"}'::jsonb,'notice','scheduled',now()-interval '1 minute',1,$3)`, []any{tenantA, dueID, actorA}},
	} {
		if _, err := adminDB.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed announcement fixture: %v\nSQL: %s", err, row.sql)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &handlers{d: Deps{Pool: app, Log: logger}}
	create := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, "", map[string]any{
		"title": "Draft announcement", "body": "Draft body", "severity": "info",
		"target_plan_ids": []string{planA}, "publish": false, "expected_version": 0,
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create announcement status=%d body=%s", create.Code, create.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" || created.Version != 1 {
		t.Fatalf("decode created announcement result=%+v err=%v body=%s", created, err, create.Body.String())
	}

	announcementState := func(id string) (status, content string, rowVersion, savedAudits, withdrawnAudits int) {
		if err := adminDB.QueryRow(ctx, `SELECT a.status,a.content::text,a.version,
			(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=a.id AND action='announcement.saved'),
			(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=a.id AND action='announcement.withdrawn')
			FROM announcements a WHERE a.id=$1`, id, tenantA).
			Scan(&status, &content, &rowVersion, &savedAudits, &withdrawnAudits); err != nil {
			t.Fatalf("read announcement state %s: %v", id, err)
		}
		return
	}
	status, content, rowVersion, savedAudits, _ := announcementState(created.ID)
	if status != "draft" || rowVersion != 1 || savedAudits != 1 {
		t.Fatalf("created state status=%s version=%d audits=%d", status, rowVersion, savedAudits)
	}

	revokeAudit := func() {
		if _, err := adminDB.Exec(ctx, `REVOKE INSERT ON audit_events FROM aegis_app`); err != nil {
			t.Fatalf("revoke audit insert: %v", err)
		}
	}
	restoreAudit := func() {
		if _, err := adminDB.Exec(ctx, `GRANT INSERT ON audit_events TO aegis_app`); err != nil {
			t.Fatalf("restore audit insert: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = adminDB.Exec(context.Background(), `GRANT INSERT ON audit_events TO aegis_app`) })

	revokeAudit()
	fault := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, created.ID, map[string]any{
		"title": "Must roll back", "body": "Changed body", "severity": "warning",
		"target_plan_ids": []string{planA}, "publish": false, "expected_version": 1,
	})
	if fault.Code != http.StatusInternalServerError {
		t.Fatalf("audit-fault update status=%d body=%s", fault.Code, fault.Body.String())
	}
	restoreAudit()
	statusAfterFault, contentAfterFault, versionAfterFault, auditsAfterFault, _ := announcementState(created.ID)
	if statusAfterFault != status || contentAfterFault != content || versionAfterFault != rowVersion || auditsAfterFault != savedAudits {
		t.Fatal("announcement update committed despite audit failure")
	}

	update := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, created.ID, map[string]any{
		"title": "Updated announcement", "body": "Updated body", "severity": "warning",
		"target_plan_ids": []string{planA}, "publish": false, "expected_version": 1,
	})
	if update.Code != http.StatusOK {
		t.Fatalf("normal update status=%d body=%s", update.Code, update.Body.String())
	}
	_, _, versionAfterUpdate, auditsAfterUpdate, _ := announcementState(created.ID)
	if versionAfterUpdate != 2 || auditsAfterUpdate != 2 {
		t.Fatalf("normal update version=%d saved_audits=%d", versionAfterUpdate, auditsAfterUpdate)
	}
	stale := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, created.ID, map[string]any{
		"title": "Stale update", "body": "Stale body", "severity": "info",
		"target_plan_ids": []string{planA}, "publish": false, "expected_version": 1,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update status=%d body=%s", stale.Code, stale.Body.String())
	}
	_, _, versionAfterStale, auditsAfterStale, _ := announcementState(created.ID)
	if versionAfterStale != 2 || auditsAfterStale != 2 {
		t.Fatal("stale update changed row or audit count")
	}

	var beforeTenantAnnouncements, beforeTenantAudits int
	if err := adminDB.QueryRow(ctx, `SELECT
		(SELECT count(*)::int FROM announcements WHERE tenant_id=$1),
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$1 AND action='announcement.saved')`, tenantA).
		Scan(&beforeTenantAnnouncements, &beforeTenantAudits); err != nil {
		t.Fatal(err)
	}
	crossPlan := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, "", map[string]any{
		"title": "Cross tenant", "body": "Must be rejected", "severity": "info",
		"target_plan_ids": []string{planB}, "publish": false, "expected_version": 0,
	})
	if crossPlan.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-tenant plan status=%d body=%s", crossPlan.Code, crossPlan.Body.String())
	}
	var afterTenantAnnouncements, afterTenantAudits int
	if err := adminDB.QueryRow(ctx, `SELECT
		(SELECT count(*)::int FROM announcements WHERE tenant_id=$1),
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$1 AND action='announcement.saved')`, tenantA).
		Scan(&afterTenantAnnouncements, &afterTenantAudits); err != nil {
		t.Fatal(err)
	}
	if afterTenantAnnouncements != beforeTenantAnnouncements || afterTenantAudits != beforeTenantAudits {
		t.Fatal("cross-tenant plan changed announcement or audit state")
	}

	notifier := notify.New(app, logger, nil)
	revokeAudit()
	if n, err := notifier.PublishDueAnnouncements(ctx, tenantA); err == nil || n != 0 {
		t.Fatalf("scheduled audit fault n=%d err=%v, want rollback", n, err)
	}
	restoreAudit()
	var dueStatus string
	var dueVersion, dueAudits int
	var duePublishedAt *time.Time
	if err := adminDB.QueryRow(ctx, `SELECT status,version,published_at,
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=$1 AND action='announcement.published')
		FROM announcements WHERE id=$1`, dueID, tenantA).Scan(&dueStatus, &dueVersion, &duePublishedAt, &dueAudits); err != nil {
		t.Fatal(err)
	}
	if dueStatus != "scheduled" || dueVersion != 1 || duePublishedAt != nil || dueAudits != 0 {
		t.Fatal("scheduled announcement committed despite audit failure")
	}
	if n, err := notifier.PublishDueAnnouncements(ctx, tenantA); err != nil || n != 1 {
		t.Fatalf("publish scheduled announcement n=%d err=%v", n, err)
	}
	if err := adminDB.QueryRow(ctx, `SELECT status,version,published_at,
		(SELECT count(*)::int FROM audit_events WHERE tenant_id=$2 AND resource_id=$1 AND action='announcement.published')
		FROM announcements WHERE id=$1`, dueID, tenantA).Scan(&dueStatus, &dueVersion, &duePublishedAt, &dueAudits); err != nil {
		t.Fatal(err)
	}
	if dueStatus != "published" || dueVersion != 2 || duePublishedAt == nil || dueAudits != 1 {
		t.Fatalf("scheduled publication state=%s version=%d published=%v audits=%d", dueStatus, dueVersion, duePublishedAt, dueAudits)
	}

	publishedToDraft := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, dueID, map[string]any{
		"title": "Cannot hide", "body": "Must withdraw", "severity": "notice",
		"publish": false, "expected_version": 2,
	})
	if publishedToDraft.Code != http.StatusConflict {
		t.Fatalf("published-to-draft status=%d body=%s", publishedToDraft.Code, publishedToDraft.Body.String())
	}
	revokeAudit()
	withdrawFault := callAnnouncementHandler(t, h.withdrawAnnouncement, tenantA, actorA, dueID, map[string]any{"expected_version": 2})
	if withdrawFault.Code != http.StatusInternalServerError {
		t.Fatalf("withdraw audit fault status=%d body=%s", withdrawFault.Code, withdrawFault.Body.String())
	}
	restoreAudit()
	dueStatus, _, dueVersion, _, dueWithdrawAudits := announcementState(dueID)
	if dueStatus != "published" || dueVersion != 2 || dueWithdrawAudits != 0 {
		t.Fatal("withdraw committed despite audit failure")
	}
	withdraw := callAnnouncementHandler(t, h.withdrawAnnouncement, tenantA, actorA, dueID, map[string]any{"expected_version": 2})
	if withdraw.Code != http.StatusOK {
		t.Fatalf("withdraw status=%d body=%s", withdraw.Code, withdraw.Body.String())
	}
	dueStatus, _, dueVersion, _, dueWithdrawAudits = announcementState(dueID)
	if dueStatus != "withdrawn" || dueVersion != 3 || dueWithdrawAudits != 1 {
		t.Fatalf("withdraw state=%s version=%d audits=%d", dueStatus, dueVersion, dueWithdrawAudits)
	}
	revive := callAnnouncementHandler(t, h.saveAnnouncement, tenantA, actorA, dueID, map[string]any{
		"title": "Cannot revive", "body": "Create a new announcement", "severity": "notice",
		"publish": true, "expected_version": 3,
	})
	if revive.Code != http.StatusConflict {
		t.Fatalf("withdrawn revive status=%d body=%s", revive.Code, revive.Body.String())
	}

	var crossTenantAnnouncements, crossTenantAudits int
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantB, ActorID: actorB}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT count(*)::int FROM announcements WHERE tenant_id=$1),
			(SELECT count(*)::int FROM audit_events WHERE tenant_id=$1)`, tenantA).
			Scan(&crossTenantAnnouncements, &crossTenantAudits)
	}); err != nil {
		t.Fatalf("cross-tenant announcement RLS query: %v", err)
	}
	if crossTenantAnnouncements != 0 || crossTenantAudits != 0 {
		t.Fatalf("cross-tenant RLS leaked announcements=%d audits=%d", crossTenantAnnouncements, crossTenantAudits)
	}
	var forced bool
	if err := app.QueryRow(ctx, `SELECT bool_and(relrowsecurity AND relforcerowsecurity)
		FROM pg_class WHERE oid IN ('announcements'::regclass,'audit_events'::regclass,'plans'::regclass)`).Scan(&forced); err != nil || !forced {
		t.Fatalf("announcement FORCE RLS proof failed forced=%v err=%v", forced, err)
	}
	t.Log("announcement_pg18_business=ok role=aegis_app rls=on schema=40 create=ok audit_rollback=save,withdraw,scheduler cas=ok lifecycle=ok cross_plan=blocked")
}

func callAnnouncementHandler(t *testing.T, handler http.HandlerFunc, tenantID, actorID, id string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/announcements", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	ctx := httpx.WithTenantID(request.Context(), tenantID)
	ctx = httpx.WithPrincipal(ctx, &httpx.Principal{
		Kind: "admin", Audience: "admin", UserID: actorID, TenantID: tenantID,
		Permissions: []string{"ops.announcement.write"}, ReauthedRecently: true,
	})
	ctx = httpx.WithRequestID(ctx, "announcement-pg18")
	if id != "" {
		routeContext := chi.NewRouteContext()
		routeContext.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request.WithContext(ctx))
	return recorder
}
