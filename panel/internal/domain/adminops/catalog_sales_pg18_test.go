package adminops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const catalogSalesPG18Fixture = "disposable-v1"

type catalogSalesSnapshot struct {
	planCode, productCode                        string
	rowVersion                                   int64
	allowNewPurchase, allowRenewal, allowUpgrade bool
	auditCount                                   int
}

type catalogSalesIdentityQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func TestUpdatePlanP0BSalesGateOrderPG18(t *testing.T) {
	fixture := strings.TrimSpace(os.Getenv("AEGIS_CATALOG_SALES_PG18_FIXTURE"))
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_CATALOG_SALES_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_CATALOG_SALES_PG18_ADMIN_DSN"))
	expectedDatabase := strings.TrimSpace(os.Getenv("AEGIS_CATALOG_SALES_PG18_DATABASE"))
	runID := strings.TrimSpace(os.Getenv("AEGIS_CATALOG_SALES_PG18_RUN_ID"))
	if fixture == "" && appDSN == "" && adminDSN == "" && expectedDatabase == "" && runID == "" {
		t.Skip("catalog sales PostgreSQL 18 fixture is not configured")
	}
	if fixture != catalogSalesPG18Fixture || appDSN == "" || adminDSN == "" || expectedDatabase == "" || runID == "" {
		t.Fatal("disposable-v1, app/admin DSNs, exact database and run ID are required")
	}
	if !strings.HasPrefix(expectedDatabase, "pandora_catalog_sales_") ||
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

	type fixtureIdentity struct {
		database, marker, systemIdentifier, databaseOID, databaseComment string
		version                                                          int
	}
	readIdentity := func(query catalogSalesIdentityQuerier) fixtureIdentity {
		var got fixtureIdentity
		err := query.QueryRow(ctx, `
			SELECT current_database(), current_setting('server_version_num')::int,
			       (SELECT run_id FROM public.pandora_catalog_sales_test_marker),
			       (SELECT system_identifier FROM public.pandora_catalog_sales_test_marker),
			       (SELECT database_oid::text FROM public.pandora_catalog_sales_test_marker),
			       shobj_description(oid, 'pg_database')
			  FROM pg_database WHERE datname=current_database()`).Scan(
			&got.database, &got.version, &got.marker, &got.systemIdentifier,
			&got.databaseOID, &got.databaseComment)
		if err != nil {
			t.Fatalf("identify PostgreSQL fixture: %v", err)
		}
		return got
	}
	adminIdentity := readIdentity(admin)
	appIdentity := readIdentity(app)
	wantComment := "pandora-catalog-sales-pg18:" + runID
	if adminIdentity.database != expectedDatabase || adminIdentity.version/10000 != 18 ||
		adminIdentity.marker != runID || adminIdentity.systemIdentifier == "" ||
		adminIdentity.databaseOID == "" || adminIdentity.databaseComment != wantComment {
		t.Fatalf("refusing unexpected fixture: %+v", adminIdentity)
	}
	if appIdentity != adminIdentity {
		t.Fatalf("refusing mismatched application fixture admin=%+v app=%+v", adminIdentity, appIdentity)
	}
	var currentRole, sessionRole, rowSecurity string
	var currentSuper, currentBypass, sessionSuper, sessionBypass bool
	if err := app.QueryRow(ctx, `SELECT current_user, session_user, cr.rolsuper, cr.rolbypassrls,
		       sr.rolsuper, sr.rolbypassrls, current_setting('row_security')
		  FROM pg_roles cr JOIN pg_roles sr ON sr.rolname=session_user
		 WHERE cr.rolname=current_user`).Scan(&currentRole, &sessionRole, &currentSuper,
		&currentBypass, &sessionSuper, &sessionBypass, &rowSecurity); err != nil {
		t.Fatalf("prove application role: %v", err)
	}
	if currentRole != "aegis_app" || sessionRole != currentRole || currentSuper || currentBypass || sessionSuper || sessionBypass || rowSecurity != "on" {
		t.Fatalf("unsafe application role current=%q session=%q super=%v/%v bypass=%v/%v row_security=%q",
			currentRole, sessionRole, currentSuper, sessionSuper, currentBypass, sessionBypass, rowSecurity)
	}
	var forcedRLS bool
	if err := app.QueryRow(ctx, `SELECT bool_and(relrowsecurity AND relforcerowsecurity)
		FROM pg_class WHERE oid IN ('products'::regclass,'plans'::regclass,'plan_versions'::regclass,'audit_events'::regclass)`).Scan(&forcedRLS); err != nil || !forcedRLS {
		t.Fatalf("catalog FORCE RLS proof failed forced=%v err=%v", forcedRLS, err)
	}

	const (
		tenantID  = "72000000-0000-7000-8000-000000000001"
		decoyID   = "72000000-0000-7000-8000-000000000002"
		actorID   = "72000000-0000-7000-8000-000000000011"
		productID = "72000000-0000-7000-8000-000000000021"
		planID    = "72000000-0000-7000-8000-000000000031"
		decoyProd = "72000000-0000-7000-8000-000000000022"
		decoyPlan = "72000000-0000-7000-8000-000000000032"
		// 套餐版本：00035 之后 active 套餐必须指向一个已冻结的版本
		planVersion  = "72000000-0000-7000-8000-000000000041"
		decoyVersion = "72000000-0000-7000-8000-000000000042"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'catalog-sales-pg18', 'Catalog Sales PG18', 'USD')`, []any{tenantID}},
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'catalog-sales-decoy', 'Catalog Sales Decoy', 'USD')`, []any{decoyID}},
		{`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($2, $1, 'catalog-sales-old', 'Catalog Sales Old', 'active')`, []any{tenantID, productID}},
		{`INSERT INTO products (id, tenant_id, code, name, status) VALUES ($2, $1, 'catalog-sales-decoy', 'Catalog Sales Decoy', 'active')`, []any{decoyID, decoyProd}},
		// 套餐要按发布流程建：先 draft，再有版本，最后才能转 active。
		//
		// 00035 给 plans 加了生命周期守卫——status='active' 且 current_version_id
		// 为空会被直接拒。这份 fixture 写在那道守卫之前，当时一条 INSERT 就能
		// 直接造出一个 active 套餐。
		{`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, allow_new_purchase, allow_renewal, allow_upgrade, status, row_version)
		  VALUES ($3, $1, $2, 'catalog-sales-old', 'Catalog Sales Old', 'public', false, false, false, 'draft', 7)`, []any{tenantID, productID, planID}},
		{`INSERT INTO plans (id, tenant_id, product_id, code, name, visibility, allow_new_purchase, allow_renewal, allow_upgrade, status, row_version)
		  VALUES ($3, $1, $2, 'catalog-sales-decoy', 'Catalog Sales Decoy', 'public', false, false, false, 'draft', 7)`, []any{decoyID, decoyProd, decoyPlan}},
		{`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($3, $1, $2, 1)`,
			[]any{tenantID, planID, planVersion}},
		{`INSERT INTO plan_versions (id, tenant_id, plan_id, version) VALUES ($3, $1, $2, 1)`,
			[]any{decoyID, decoyPlan, decoyVersion}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE id=$1`, []any{planVersion}},
		{`UPDATE plan_versions SET frozen_at=now() WHERE id=$1`, []any{decoyVersion}},
		{`UPDATE plans SET current_version_id=$2, status='active' WHERE id=$1`,
			[]any{planID, planVersion}},
		{`UPDATE plans SET current_version_id=$2, status='active' WHERE id=$1`,
			[]any{decoyPlan, decoyVersion}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed catalog sales fixture: %v", err)
		}
	}
	if err := app.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM plans WHERE tenant_id=$1`, decoyID).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			return fmt.Errorf("tenant RLS exposed %d decoy plans", visible)
		}
		return nil
	}); err != nil {
		t.Fatalf("prove tenant RLS: %v", err)
	}

	readSnapshot := func() catalogSalesSnapshot {
		var got catalogSalesSnapshot
		if err := admin.QueryRow(ctx, `
			SELECT p.code, pr.code, p.row_version,
			       p.allow_new_purchase, p.allow_renewal, p.allow_upgrade,
			       (SELECT count(*)::int FROM audit_events ae WHERE ae.tenant_id=p.tenant_id)
			  FROM plans p JOIN products pr
			    ON pr.tenant_id=p.tenant_id AND pr.id=p.product_id
			 WHERE p.tenant_id=$1 AND p.id=$2`, tenantID, planID).Scan(
			&got.planCode, &got.productCode, &got.rowVersion,
			&got.allowNewPurchase, &got.allowRenewal, &got.allowUpgrade,
			&got.auditCount); err != nil {
			t.Fatalf("read catalog sales snapshot: %v", err)
		}
		return got
	}
	before := readSnapshot()
	if before.rowVersion != 7 || before.auditCount != 0 {
		t.Fatalf("unexpected initial snapshot: %+v", before)
	}
	input := UpdatePlanInput{
		ActorID: actorID, ExpectedRowVersion: 6,
		Code: "catalog-sales-new", Name: "Catalog Sales New", Visibility: "public",
		AllowNewPurchase: true,
	}

	if _, err := NewService(app).UpdatePlan(ctx, tenantID, planID, input); err == nil {
		t.Fatal("default-deny catalog update unexpectedly succeeded")
	} else {
		requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
		var denied *httpx.Error
		if !errors.As(err, &denied) || denied.Message != "服务暂时不可用，请稍后重试" {
			t.Fatalf("default denial is not neutral: %v", err)
		}
	}
	if afterDenied := readSnapshot(); afterDenied != before {
		t.Fatalf("denied stale update changed persisted state before=%+v after=%+v", before, afterDenied)
	}

	if _, err := NewService(app, staticSalesCapability(true)).UpdatePlan(ctx, tenantID, planID, input); err == nil {
		t.Fatal("authorized stale catalog update unexpectedly succeeded")
	} else {
		requireCatalogErrorCode(t, err, httpx.CodeConflict)
		var conflict *httpx.Error
		if !errors.As(err, &conflict) || conflict.Fields["row_version"] != "current=7" {
			t.Fatalf("authorized stale response did not expose current version: %v", err)
		}
	}
	if afterConflict := readSnapshot(); afterConflict != before {
		t.Fatalf("authorized stale conflict changed persisted state before=%+v after=%+v", before, afterConflict)
	}

	t.Log("catalog_sales_pg18_business=ok unavailable_before_stale=true denied_writes=0 denied_audits=0 allowed_stale=conflict role=aegis_app rls=on schema=41")
}
