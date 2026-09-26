// [INPUT]: 依赖 platform/db 与 pgx 的双连接（夹具管理员 + aegis_app），依赖 middleware 幂等声明，依赖迁移 00040 的 app.order_release_00040_meta 水位
// [OUTPUT]: 包内提供库身份护栏 orderReleasePG18AssertRuntimeTarget、一次性租户夹具 orderReleasePG18Fixture / orderReleasePG18Seed、幂等认领 orderReleasePG18Claim、回拨时间、释放故障注入与重试证据、业务快照、充值、事务工具 orderReleasePG18InTx / InTxAs、指纹与 SQLSTATE 判定
// [POS]: 本包 PG18 测试的夹具库：TestOrderReleasePG18 及挂在它上面的佣金、手工订单子用例，以及 plan_change、traffic_pack、ineligible_settlement 等域都从这里取夹具
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/middleware"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func orderReleasePG18AssertRuntimeTarget(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, admin *pgx.Conn) {
	t.Helper()
	var adminDatabase, adminOID, systemIdentifier string
	if err := admin.QueryRow(ctx, `SELECT current_database(),
		(SELECT oid::text FROM pg_database WHERE datname=current_database()),
		(SELECT system_identifier::text FROM pg_control_system())`).Scan(
		&adminDatabase, &adminOID, &systemIdentifier); err != nil {
		t.Fatalf("read PostgreSQL target identity: %v", err)
	}
	tables := []string{
		"users", "products", "plans", "plan_versions", "prices",
		"entitlements", "quota_definitions", "orders", "order_items",
		"order_reservations", "order_reservation_events", "order_stock_reservations",
		"order_purchase_limit_reservations", "plan_purchase_counters", "coupons",
		"coupon_redemptions", "balance_holds", "ledger_accounts",
		"ledger_transactions", "ledger_entries", "audit_events", "idempotency_keys",
		"payment_providers", "payment_intents", "payment_events", "payments",
		"late_payment_cases", "subscriptions", "quota_balances", "referrals",
		"system_settings", "commission_entries", "withdrawals",
	}
	orderReleasePG18InTx(t, ctx, pool, uuid.NewString(), func(tx pgx.Tx) error {
		var user, database, oid string
		var superuser, bypassRLS bool
		if err := tx.QueryRow(ctx, `SELECT current_user,current_database(),
			(SELECT oid::text FROM pg_database WHERE datname=current_database()),
			rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(
			&user, &database, &oid, &superuser, &bypassRLS); err != nil {
			return err
		}
		if user != "aegis_app" || superuser || bypassRLS ||
			database != adminDatabase || oid != adminOID {
			t.Fatalf("runtime target user=%s super=%t bypass_rls=%t database=%s/%s oid=%s/%s system=%s",
				user, superuser, bypassRLS, database, adminDatabase, oid, adminOID, systemIdentifier)
		}
		var protected int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname='public' AND c.relname=ANY($1::text[])
			  AND c.relrowsecurity AND c.relforcerowsecurity
			  AND pg_get_userbyid(c.relowner)<>current_user`, tables).Scan(&protected); err != nil {
			return err
		}
		if protected != len(tables) {
			t.Fatalf("runtime protected table count=%d want=%d", protected, len(tables))
		}
		return nil
	})
	t.Logf("marker=order_release_pg18_target_identity_ok database=%s oid=%s system_identifier=%s role=aegis_app rls=forced",
		adminDatabase, adminOID, systemIdentifier)
}

func orderReleasePG18WaitLockedBatch(t *testing.T, batches <-chan []string,
	worker string) []string {
	t.Helper()
	select {
	case ids := <-batches:
		return ids
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not reach the candidate-lock synchronization point", worker)
		return nil
	}
}

type orderReleasePG18Fixture struct {
	tenant, shadowTenant, buyer, commissionBuyer, referrer, shadowUser string
	product, plan, version, price, coupon, couponCode                  string
	provider, providerCode, suffix                                     string
}

func orderReleasePG18Seed(t *testing.T, ctx context.Context, admin *pgx.Conn) orderReleasePG18Fixture {
	t.Helper()
	compact := strings.ReplaceAll(uuid.NewString(), "-", "")
	fx := orderReleasePG18Fixture{
		tenant: uuid.NewString(), shadowTenant: uuid.NewString(), buyer: uuid.NewString(),
		commissionBuyer: uuid.NewString(), referrer: uuid.NewString(), shadowUser: uuid.NewString(),
		product: uuid.NewString(), plan: uuid.NewString(), version: uuid.NewString(),
		price: uuid.NewString(), coupon: uuid.NewString(), couponCode: "ORP100-" + compact[:8],
		provider: uuid.NewString(), providerCode: "orp18-" + compact[:12],
		suffix: compact[:16],
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fixture: %v", err)
	}
	defer tx.Rollback(ctx)
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed fixture: %v\nSQL: %s", err, query)
		}
	}
	must(`INSERT INTO tenants(id,slug,display_name,default_currency)
		VALUES($1,$2,$3,'CNY'),($4,$5,$6,'CNY')`,
		fx.tenant, "orp18-"+compact[:12], "Order Release PG18",
		fx.shadowTenant, "orp18-shadow-"+compact[:8], "Order Release PG18 Shadow")
	must(`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES
		($1,$2,$3,'Release Buyer','active'),
		($4,$2,$5,'Commission Buyer','active'),
		($6,$2,$7,'Commission Referrer','active'),
		($8,$9,$10,'Shadow User','active')`,
		fx.buyer, fx.tenant, "buyer-"+compact[:12]+"@example.test",
		fx.commissionBuyer, "commission-buyer-"+compact[:12]+"@example.test",
		fx.referrer, "referrer-"+compact[:12]+"@example.test",
		fx.shadowUser, fx.shadowTenant, "shadow-"+compact[:12]+"@example.test")
	must(`INSERT INTO products(id,tenant_id,code,name,status)
		VALUES($1,$2,$3,'Order Release Product','active')`, fx.product, fx.tenant, "orp-"+compact[:10])
	must(`INSERT INTO plans(id,tenant_id,product_id,code,name,status,
		purchase_limit_per_user,stock_total)
		VALUES($1,$2,$3,$4,'Order Release Plan','draft',10,50)`,
		fx.plan, fx.tenant, fx.product, "orp-plan-"+compact[:8])
	must(`INSERT INTO plan_versions(id,tenant_id,plan_id,version) VALUES($1,$2,$3,1)`,
		fx.version, fx.tenant, fx.plan)
	must(`UPDATE plan_versions SET frozen_at=now(),status='published'
		WHERE tenant_id=$1 AND id=$2`, fx.tenant, fx.version)
	must(`UPDATE plans SET current_version_id=$3,status='active' WHERE tenant_id=$1 AND id=$2`,
		fx.tenant, fx.plan, fx.version)
	must(`INSERT INTO prices(id,tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status)
		VALUES($1,$2,$3,'CNY',1000,'month',1,'active')`, fx.price, fx.tenant, fx.product)
	must(`INSERT INTO coupons(id,tenant_id,code,name,discount_type,discount_value,
		currency,applicable_plan_ids,max_redemptions,max_redemptions_per_user,status)
		VALUES($1,$2,$3,'Order Release Coupon','fixed',100,'CNY',ARRAY[$4::uuid],10,10,'active')`,
		fx.coupon, fx.tenant, fx.couponCode, fx.plan)
	must(`INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
		VALUES($1,$2,$3,'demo_hmac','Order Release Provider',true),
		      ($4,$5,$6,'demo_hmac','Shadow Provider',true)`,
		fx.provider, fx.tenant, fx.providerCode, uuid.NewString(), fx.shadowTenant, "shadow-"+fx.providerCode)
	must(`INSERT INTO referrals(tenant_id,referee_user_id,referrer_user_id,channel,campaign)
		VALUES($1,$2,$3,'pg18','order-release')`, fx.tenant, fx.commissionBuyer, fx.referrer)
	must(`INSERT INTO system_settings(tenant_id,key,value,value_schema) VALUES
		($1,'commission.rate_percent','10'::jsonb,'{"type":"integer","minimum":0,"maximum":50}'::jsonb),
		($1,'commission.freeze_days','0'::jsonb,'{"type":"integer","minimum":0,"maximum":90}'::jsonb),
		($1,'commission.min_withdraw','100'::jsonb,'{"type":"integer","minimum":0}'::jsonb)`, fx.tenant)
	must(`SET CONSTRAINTS ALL IMMEDIATE`)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
	return fx
}

func orderReleasePG18Claim(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, actorID, logicalScope, label string) middleware.IdempotencyClaim {
	t.Helper()
	id := uuid.NewString()
	key := "orp18-" + label + "-" + id[:8]
	hash := sha256.Sum256([]byte("order-release-pg18:" + label + ":" + id))
	actorHash := sha256.Sum256([]byte(actorID))
	storageScope := fmt.Sprintf("%s:actor:%x", logicalScope, actorHash[:12])
	lease := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := admin.Exec(ctx, `INSERT INTO idempotency_keys
		(id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
		VALUES($1,$2,$3,$4,$5,'in_flight',$6,$7)`,
		id, tenantID, storageScope, key, hash[:], actorID, lease); err != nil {
		t.Fatalf("insert idempotency claim %s: %v", label, err)
	}
	return middleware.IdempotencyClaim{
		ID: id, TenantID: tenantID, ActorID: actorID, Scope: logicalScope,
		StorageScope: storageScope, Key: key, RequestHash: hash,
		Generation: 1, LockedUntil: lease,
	}
}

func orderReleasePG18Backdate(t *testing.T, ctx context.Context, admin *pgx.Conn,
	tenantID, orderID string) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin backdate: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable user triggers for fixture backdate: %v", err)
	}
	if tag, err := tx.Exec(ctx, `UPDATE orders SET expires_at=now()-interval '1 minute'
		WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, orderID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate order tag=%v err=%v", tag, err)
	}
	if tag, err := tx.Exec(ctx, `UPDATE order_reservations SET expires_at=now()-interval '1 minute'
		WHERE tenant_id=$1 AND order_id=$2::uuid`, tenantID, orderID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("backdate reservation tag=%v err=%v", tag, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit backdate: %v", err)
	}
}

func orderReleasePG18InsertActiveIntent(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) {
	t.Helper()
	intentID := uuid.NewString()
	providerRef := "orp18-fault-" + fx.suffix
	if tag, err := admin.Exec(ctx, `INSERT INTO payment_intents
		(id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
		VALUES($1,$2,$3::uuid,$4,'CNY',700,'requires_action',$5)`,
		intentID, fx.tenant, orderID, fx.provider, providerRef); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("insert active fault-test payment intent tag=%v err=%v", tag, err)
	}
}

func orderReleasePG18AssertSchema40Used(t *testing.T, ctx context.Context,
	admin *pgx.Conn, want bool) {
	t.Helper()
	var got bool
	if err := admin.QueryRow(ctx, `SELECT used FROM app.order_release_00040_meta WHERE singleton`).
		Scan(&got); err != nil {
		t.Fatalf("read schema40 order-release watermark: %v", err)
	}
	if got != want {
		t.Fatalf("schema40 order-release watermark=%t want=%t", got, want)
	}
}

func orderReleasePG18AssertFaultAttempt(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, want int64) {
	t.Helper()
	sequence := pgx.Identifier{"orp_fault_" + fx.suffix, "attempt_seq"}.Sanitize()
	var got int64
	var called bool
	if err := admin.QueryRow(ctx, "SELECT last_value,is_called FROM "+sequence).
		Scan(&got, &called); err != nil {
		t.Fatalf("read release fault attempt: %v", err)
	}
	if got != want || !called {
		t.Fatalf("release fault attempt=%d/%t want=%d/true", got, called, want)
	}
}

func orderReleasePG18InstallReleaseFault(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, admin *pgx.Conn, fx orderReleasePG18Fixture,
	orderID string) func() error {
	t.Helper()

	schemaName := "orp_fault_" + fx.suffix
	tableName := "control"
	sequenceName := "attempt_seq"
	functionName := "fail_first_cancel"
	triggerName := "trg_orp_fault_" + fx.suffix
	schemaID := pgx.Identifier{schemaName}.Sanitize()
	tableID := pgx.Identifier{tableName}.Sanitize()
	sequenceID := pgx.Identifier{sequenceName}.Sanitize()
	functionID := pgx.Identifier{functionName}.Sanitize()
	triggerID := pgx.Identifier{triggerName}.Sanitize()
	qualifiedTable := schemaID + "." + tableID
	qualifiedSequence := schemaID + "." + sequenceID
	qualifiedFunction := schemaID + "." + functionID

	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin release fault installation: %v", err)
	}
	defer tx.Rollback(ctx)
	must := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(ctx, query, args...); execErr != nil {
			t.Fatalf("install release fault: %v\nSQL: %s", execErr, query)
		}
	}
	must("CREATE SCHEMA " + schemaID)
	must("REVOKE ALL ON SCHEMA " + schemaID + " FROM PUBLIC, aegis_app")
	must("CREATE TABLE " + qualifiedTable + ` (
		singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		tenant_id uuid NOT NULL, order_id uuid NOT NULL, action text NOT NULL)`)
	must("INSERT INTO "+qualifiedTable+"(tenant_id,order_id,action) VALUES($1,$2::uuid,'order.cancelled')",
		fx.tenant, orderID)
	must("CREATE SEQUENCE " + qualifiedSequence + " START WITH 1 INCREMENT BY 1 NO CYCLE")
	functionSQL := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger
		LANGUAGE plpgsql SECURITY DEFINER
		SET search_path = pg_catalog
		AS $fault$
		DECLARE target_tenant uuid; target_order uuid; target_action text;
		BEGIN
		  IF TG_RELID <> 'public.audit_events'::pg_catalog.regclass OR TG_OP <> 'INSERT' THEN
		    RAISE EXCEPTION 'order release fault trigger target mismatch' USING ERRCODE='55000';
		  END IF;
		  SELECT tenant_id,order_id,action INTO STRICT target_tenant,target_order,target_action
		    FROM %s WHERE singleton;
		  IF NEW.tenant_id=target_tenant AND NEW.resource_id=target_order
		     AND NEW.resource_type='order' AND NEW.action=target_action
		     AND pg_catalog.nextval('%s'::pg_catalog.regclass)=1 THEN
		    RAISE EXCEPTION 'order release rollback test fault' USING ERRCODE='P0001';
		  END IF;
		  RETURN NULL;
		END
		$fault$`, qualifiedFunction, qualifiedTable, qualifiedSequence)
	must(functionSQL)
	must("REVOKE ALL ON TABLE " + qualifiedTable + " FROM PUBLIC, aegis_app")
	must("REVOKE ALL ON SEQUENCE " + qualifiedSequence + " FROM PUBLIC, aegis_app")
	must("REVOKE ALL ON FUNCTION " + qualifiedFunction + "() FROM PUBLIC, aegis_app")
	must(fmt.Sprintf(`CREATE CONSTRAINT TRIGGER %s
		AFTER INSERT ON public.audit_events
		DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
		EXECUTE FUNCTION %s()`, triggerID, qualifiedFunction))
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit release fault installation: %v", err)
	}

	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupTx, beginErr := admin.Begin(ctx)
			if beginErr != nil {
				cleanupErr = fmt.Errorf("begin: %w", beginErr)
				return
			}
			defer cleanupTx.Rollback(ctx)
			statements := []string{
				"DROP TRIGGER " + triggerID + " ON public.audit_events",
				"DROP FUNCTION " + qualifiedFunction + "()",
				"DROP SEQUENCE " + qualifiedSequence,
				"DROP TABLE " + qualifiedTable,
				"DROP SCHEMA " + schemaID,
			}
			for _, statement := range statements {
				if _, execErr := cleanupTx.Exec(ctx, statement); execErr != nil {
					cleanupErr = fmt.Errorf("%s: %w", statement, execErr)
					return
				}
			}
			var remaining int
			if scanErr := cleanupTx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM pg_trigger WHERE tgname=$1 AND NOT tgisinternal)+
				(SELECT count(*) FROM pg_namespace WHERE nspname=$2)`,
				triggerName, schemaName).Scan(&remaining); scanErr != nil {
				cleanupErr = fmt.Errorf("verify absence: %w", scanErr)
				return
			}
			if remaining != 0 {
				cleanupErr = fmt.Errorf("fault objects remain: %d", remaining)
				return
			}
			if commitErr := cleanupTx.Commit(ctx); commitErr != nil {
				cleanupErr = fmt.Errorf("commit: %w", commitErr)
			}
		})
		return cleanupErr
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("release fault cleanup: %v", err)
		}
	})

	var schemaUsage, schemaCreate bool
	var sequenceUsage, sequenceSelect, sequenceUpdate bool
	var tableSelect, tableInsert, tableUpdate, tableDelete, tableTruncate bool
	var auditTrigger, functionExecute, securityDefiner, functionLeakproof bool
	var functionOwner, functionVolatility string
	var functionConfig []string
	functionSignature := schemaName + "." + functionName + "()"
	if err := admin.QueryRow(ctx, `SELECT
		has_schema_privilege('aegis_app',$1,'USAGE'),
		has_schema_privilege('aegis_app',$1,'CREATE'),
		has_sequence_privilege('aegis_app',$2,'USAGE'),
		has_sequence_privilege('aegis_app',$2,'SELECT'),
		has_sequence_privilege('aegis_app',$2,'UPDATE'),
		has_table_privilege('aegis_app',$3,'SELECT'),
		has_table_privilege('aegis_app',$3,'INSERT'),
		has_table_privilege('aegis_app',$3,'UPDATE'),
		has_table_privilege('aegis_app',$3,'DELETE'),
		has_table_privilege('aegis_app',$3,'TRUNCATE'),
		has_table_privilege('aegis_app','public.audit_events','TRIGGER'),
		has_function_privilege('aegis_app',$4,'EXECUTE'),
		p.prosecdef,p.proleakproof,p.provolatile::text,r.rolname,
		coalesce(p.proconfig,ARRAY[]::text[])
		FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		JOIN pg_roles r ON r.oid=p.proowner
		WHERE n.nspname=$1 AND p.proname=$5 AND p.pronargs=0`,
		schemaName, schemaName+"."+sequenceName, schemaName+"."+tableName,
		functionSignature, functionName).Scan(
		&schemaUsage, &schemaCreate, &sequenceUsage, &sequenceSelect, &sequenceUpdate,
		&tableSelect, &tableInsert, &tableUpdate, &tableDelete, &tableTruncate,
		&auditTrigger, &functionExecute, &securityDefiner, &functionLeakproof,
		&functionVolatility, &functionOwner, &functionConfig); err != nil {
		t.Fatalf("inspect release fault privileges: %v", err)
	}
	if schemaUsage || schemaCreate || sequenceUsage || sequenceSelect || sequenceUpdate ||
		tableSelect || tableInsert || tableUpdate || tableDelete || tableTruncate ||
		auditTrigger || functionExecute || !securityDefiner || functionLeakproof ||
		functionVolatility != "v" || functionOwner == "aegis_app" ||
		len(functionConfig) != 1 || functionConfig[0] != "search_path=pg_catalog" {
		t.Fatalf("unsafe release fault privileges schema=%t/%t sequence=%t/%t/%t table=%t/%t/%t/%t/%t trigger=%t function=%t definer=%t leakproof=%t volatility=%s owner=%s config=%v",
			schemaUsage, schemaCreate, sequenceUsage, sequenceSelect, sequenceUpdate,
			tableSelect, tableInsert, tableUpdate, tableDelete, tableTruncate,
			auditTrigger, functionExecute, securityDefiner, functionLeakproof,
			functionVolatility, functionOwner, functionConfig)
	}
	var triggerRows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t
		JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace n ON n.oid=p.pronamespace
		WHERE t.tgname=$1 AND t.tgrelid='public.audit_events'::regclass
		AND NOT t.tgisinternal AND t.tgdeferrable AND t.tginitdeferred AND t.tgenabled='O'
		AND n.nspname=$2 AND p.proname=$3
		AND pg_get_triggerdef(t.oid) LIKE '%AFTER INSERT%DEFERRABLE INITIALLY DEFERRED%'`,
		triggerName, schemaName, functionName).Scan(&triggerRows); err != nil {
		t.Fatalf("inspect release fault trigger: %v", err)
	}
	if triggerRows != 1 {
		t.Fatalf("release fault trigger rows=%d want=1", triggerRows)
	}
	var sequenceOID uint32
	var sequenceLast int64
	var sequenceCalled bool
	if err := admin.QueryRow(ctx, "SELECT $1::regclass::oid,last_value,is_called FROM "+qualifiedSequence,
		schemaName+"."+sequenceName).Scan(&sequenceOID, &sequenceLast, &sequenceCalled); err != nil {
		t.Fatalf("inspect release fault sequence: %v", err)
	}
	if sequenceLast != 1 || sequenceCalled {
		t.Fatalf("release fault sequence initial=%d/%t want=1/false", sequenceLast, sequenceCalled)
	}
	permissionErr := pool.InTx(ctx, platformdb.Scope{TenantID: fx.tenant}, func(tx pgx.Tx) error {
		var value int64
		return tx.QueryRow(ctx, `SELECT pg_catalog.nextval($1::oid::pg_catalog.regclass)`, sequenceOID).
			Scan(&value)
	})
	var permissionPGErr *pgconn.PgError
	if !errors.As(permissionErr, &permissionPGErr) || permissionPGErr.Code != "42501" {
		t.Fatalf("aegis_app direct fault sequence access err=%v pg=%#v want SQLSTATE 42501",
			permissionErr, permissionPGErr)
	}
	if err := admin.QueryRow(ctx, "SELECT last_value,is_called FROM "+qualifiedSequence).
		Scan(&sequenceLast, &sequenceCalled); err != nil {
		t.Fatalf("reinspect release fault sequence: %v", err)
	}
	if sequenceLast != 1 || sequenceCalled {
		t.Fatalf("denied app probe consumed release fault sequence=%d/%t", sequenceLast, sequenceCalled)
	}
	return cleanup
}

func orderReleasePG18BusinessSnapshot(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) string {
	t.Helper()
	parts := make([]string, 0, 19)
	add := func(name, query string, args ...any) {
		t.Helper()
		var value string
		if err := admin.QueryRow(ctx, query, args...).Scan(&value); err != nil {
			t.Fatalf("snapshot %s: %v", name, err)
		}
		parts = append(parts, name+"="+value)
	}
	rowSet := func(table, predicate, order string) string {
		return fmt.Sprintf(`SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY %s),'[]'::jsonb)::text
			FROM %s x WHERE %s`, order, table, predicate)
	}
	add("orders", rowSet("orders", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("items", rowSet("order_items", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("intents", rowSet("payment_intents", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("reservations", rowSet("order_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("stock", rowSet("order_stock_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("purchase", rowSet("order_purchase_limit_reservations", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("redemptions", rowSet("coupon_redemptions", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("holds", rowSet("balance_holds", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("plans", rowSet("plans", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, fx.plan)
	add("purchase_counters", rowSet("plan_purchase_counters", "x.tenant_id=$1 AND x.plan_id=$2::uuid AND x.user_id=$3::uuid", "x.plan_id,x.user_id"), fx.tenant, fx.plan, fx.buyer)
	add("coupons", rowSet("coupons", "x.tenant_id=$1 AND x.id=$2::uuid", "x.id"), fx.tenant, fx.coupon)
	add("accounts", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM ledger_accounts x WHERE x.tenant_id=$1 AND x.id IN (
			SELECT available_account_id FROM balance_holds WHERE tenant_id=$1 AND order_id=$2::uuid
			UNION SELECT hold_account_id FROM balance_holds WHERE tenant_id=$1 AND order_id=$2::uuid)`,
		fx.tenant, orderID)
	add("ledger_transactions", rowSet("ledger_transactions", "x.tenant_id=$1 AND x.source_type='order' AND x.source_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("ledger_entries", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM ledger_entries x WHERE x.tenant_id=$1 AND x.transaction_id IN (
			SELECT id FROM ledger_transactions WHERE tenant_id=$1 AND source_type='order' AND source_id=$2::uuid)`,
		fx.tenant, orderID)
	add("events", rowSet("order_reservation_events", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("audit", rowSet("audit_events", "x.tenant_id=$1 AND x.resource_type='order' AND x.resource_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("idempotency", `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]'::jsonb)::text
		FROM idempotency_keys x WHERE x.tenant_id=$1 AND x.id=(
			SELECT idempotency_key_id FROM orders WHERE tenant_id=$1 AND id=$2::uuid)`, fx.tenant, orderID)
	add("payments", rowSet("payments", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("refunds", rowSet("refunds", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("late_cases", rowSet("late_payment_cases", "x.tenant_id=$1 AND x.order_id=$2::uuid", "x.id"), fx.tenant, orderID)
	add("schema40", `SELECT to_jsonb(x)::text FROM app.order_release_00040_meta x WHERE singleton`)
	return strings.Join(parts, "\n")
}

func orderReleasePG18AssertFaultRetryEvidence(t *testing.T, ctx context.Context,
	admin *pgx.Conn, fx orderReleasePG18Fixture, orderID string) {
	t.Helper()
	requestID := httpx.RequestIDFrom(ctx)
	var intents, cancelledIntents, events, reserveEvents, exactCancelEvents int
	var audits, createdAudits, exactCancelAudits, payments, cases, refunds int
	err := admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM payment_intents WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM payment_intents WHERE tenant_id=$1 AND order_id=$2::uuid AND status='cancelled'),
		(SELECT count(*) FROM order_reservation_events WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM order_reservation_events WHERE tenant_id=$1 AND order_id=$2::uuid AND event_kind='reserve'),
		(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=$1 AND e.order_id=$2::uuid
		 AND e.event_kind='cancel' AND e.from_state='held' AND e.to_state='released'
		 AND e.business_request_id=(SELECT business_request_id FROM orders WHERE tenant_id=$1 AND id=$2::uuid)
		 AND e.actor_kind='user' AND e.actor_id=$3::uuid AND e.reason='user_cancelled'),
		(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_type='order' AND resource_id=$2::uuid),
		(SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_type='order' AND resource_id=$2::uuid AND action='order.created'),
		(SELECT count(*) FROM audit_events a WHERE a.tenant_id=$1 AND a.resource_type='order' AND a.resource_id=$2::uuid
		 AND a.action='order.cancelled' AND a.actor_kind='user' AND a.actor_id=$3::uuid
		 AND a.request_id=$4 AND a.api_domain='public' AND a.outcome='success'),
		(SELECT count(*) FROM payments WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM late_payment_cases WHERE tenant_id=$1 AND order_id=$2::uuid),
		(SELECT count(*) FROM refunds WHERE tenant_id=$1 AND order_id=$2::uuid)`,
		fx.tenant, orderID, fx.buyer, requestID).Scan(
		&intents, &cancelledIntents, &events, &reserveEvents, &exactCancelEvents,
		&audits, &createdAudits, &exactCancelAudits, &payments, &cases, &refunds)
	if err != nil {
		t.Fatalf("read fault retry evidence: %v", err)
	}
	if intents != 1 || cancelledIntents != 1 || events != 2 || reserveEvents != 1 ||
		exactCancelEvents != 1 || audits != 2 || createdAudits != 1 || exactCancelAudits != 1 ||
		payments != 0 || cases != 0 || refunds != 0 {
		t.Fatalf("fault retry evidence intents=%d/%d events=%d/%d/%d audits=%d/%d/%d payments=%d cases=%d refunds=%d request=%s",
			intents, cancelledIntents, events, reserveEvents, exactCancelEvents,
			audits, createdAudits, exactCancelAudits, payments, cases, refunds, requestID)
	}
}

func orderReleasePG18FundBalance(t *testing.T, ctx context.Context,
	pool *platformdb.Pool, tenantID, userID string, amount int64) {
	t.Helper()
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		available, err := EnsureAccount(ctx, tx, tenantID,
			AccountUserBalance, "CNY", &userID, "")
		if err != nil {
			return err
		}
		source, err := EnsureAccount(ctx, tx, tenantID,
			AccountSuspense, "CNY", nil, "order-release-pg18-funding")
		if err != nil {
			return err
		}
		_, err = Post(ctx, tx, tenantID, Posting{
			Kind: "balance_topup", Currency: "CNY", SourceType: "test_fixture",
			Memo: "order release PG18 user balance", ActorKind: "system",
			Entries: []Entry{
				{AccountID: source, Direction: Debit, Amount: amount},
				{AccountID: available, Direction: Credit, Amount: amount},
			},
		})
		return err
	})
}

func orderReleasePG18InTx(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID string, fn func(pgx.Tx) error) {
	t.Helper()
	orderReleasePG18InTxAs(t, ctx, pool, tenantID, "", fn)
}

// orderReleasePG18InTxAs 在指定租户 + 指定操作者的作用域里跑一个事务。
//
// 光有租户不够：idempotency_keys 上挂着 idempotency_keys_actor_select 策略，
// 按 actor 再过滤一层。只设 TenantID 的话，那张表在这个事务里看起来是空的——
// AssertHeldResources 那条 11 表 JOIN 因此永远返回 no rows，而报错只说
// "no rows in result set"，看不出是哪一张表被 RLS 挡住了。
func orderReleasePG18InTxAs(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID, actorID string, fn func(pgx.Tx) error) {
	t.Helper()
	if err := pool.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID}, fn); err != nil {
		t.Fatalf("aegis_app tenant transaction: %v", err)
	}
}

func orderReleasePG18Fingerprint(t *testing.T, ctx context.Context, pool *platformdb.Pool,
	tenantID, orderID string) string {
	t.Helper()
	var status, reservation string
	var events, audits, payments, cases, txns int
	orderReleasePG18InTx(t, ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT o.status,r.state,
			(SELECT count(*) FROM order_reservation_events e WHERE e.tenant_id=o.tenant_id AND e.order_id=o.id),
			(SELECT count(*) FROM audit_events a WHERE a.tenant_id=o.tenant_id AND a.resource_id=o.id),
			(SELECT count(*) FROM payments p WHERE p.tenant_id=o.tenant_id AND p.order_id=o.id),
			(SELECT count(*) FROM late_payment_cases c WHERE c.tenant_id=o.tenant_id AND c.order_id=o.id),
			(SELECT count(*) FROM ledger_transactions l WHERE l.tenant_id=o.tenant_id AND l.source_type='order' AND l.source_id=o.id)
			FROM orders o JOIN order_reservations r ON r.tenant_id=o.tenant_id AND r.order_id=o.id
			WHERE o.tenant_id=$1 AND o.id=$2::uuid`, tenantID, orderID).
			Scan(&status, &reservation, &events, &audits, &payments, &cases, &txns)
	})
	return fmt.Sprintf("%s/%s/events=%d/audits=%d/payments=%d/cases=%d/order_txns=%d",
		status, reservation, events, audits, payments, cases, txns)
}

// orderReleasePG18RejectedByPrivilegeOrGuard 判断一次越权写是否被挡住了，
// 不在乎挡它的是权限层还是守卫触发器。
//
// 提现金额、身份和存在性都不该被运行时角色改动。原先这层保护来自列级
// GRANT，被拒时是 42501；列级白名单放弃之后（见 deploy/configure-app-role.sql
// 末尾），改由 00040 的 guard_withdrawal 触发器拦下，错误码变成 23514。
//
// 被拒这件事没变，变的只是谁来拒。断言盯着「必须被拒」，而不是盯着某一个
// 错误码——否则每换一次拦截层，一堆测试就要跟着改一遍数字。
func orderReleasePG18RejectedByPrivilegeOrGuard(err error) bool {
	switch orderReleasePG18SQLState(err) {
	case "42501", "23514":
		return true
	default:
		return false
	}
}

func orderReleasePG18SQLState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
