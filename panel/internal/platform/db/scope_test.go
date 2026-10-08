package db

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

const (
	scopeTestTenant = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	scopeTestActor  = "22222222-2222-4222-8222-222222222222"
)

// BEGIN 与注入租户上下文合成一次往返：内联的只能是规范 UUID，set_config 必须是事务级。
func TestScopedBeginSQLInlinesOnlyCanonicalUUIDs(t *testing.T) {
	got, ok := scopedBeginSQL(Scope{TenantID: scopeTestTenant, ActorID: scopeTestActor}, "", false)
	want := "BEGIN; SELECT set_config('app.tenant_id', '" + scopeTestTenant +
		"', true), set_config('app.actor_id', '" + scopeTestActor + "', true)"
	if !ok || got != want {
		t.Fatalf("scopedBeginSQL = %q, %v\nwant %q", got, ok, want)
	}

	got, ok = scopedBeginSQL(Scope{TenantID: scopeTestTenant}, pgx.Serializable, false)
	if !ok || !strings.HasPrefix(got, "BEGIN ISOLATION LEVEL SERIALIZABLE; SELECT set_config(") ||
		!strings.HasSuffix(got, "set_config('app.actor_id', '', true)") {
		t.Fatalf("serializable anonymous scope = %q, %v", got, ok)
	}

	// 重试排队：LOCK 紧跟 BEGIN、在 set_config 之前（LOCK 不取快照，set_config 取）
	got, ok = scopedBeginSQL(Scope{TenantID: scopeTestTenant}, pgx.Serializable, true)
	if !ok || !strings.HasPrefix(got, "BEGIN ISOLATION LEVEL SERIALIZABLE; "+chainGateSQL+"; SELECT set_config(") {
		t.Fatalf("gated serializable scope = %q, %v", got, ok)
	}

	for _, s := range []Scope{
		{TenantID: strings.ToUpper(scopeTestTenant)},
		{TenantID: "{" + scopeTestTenant + "}"},
		{TenantID: "urn:uuid:" + scopeTestTenant},
		{TenantID: strings.ReplaceAll(scopeTestTenant, "-", "")},
		{TenantID: scopeTestTenant + "'; DROP TABLE users; --"},
		{TenantID: scopeTestTenant, ActorID: "system"},
		{TenantID: scopeTestTenant, ActorID: "x', false); --"},
	} {
		if sql, ok := scopedBeginSQL(s, "", false); ok {
			t.Errorf("non-canonical scope %+v was inlined: %s", s, sql)
		}
	}
	if _, ok := scopedBeginSQL(Scope{TenantID: scopeTestTenant}, pgx.RepeatableRead, false); ok {
		t.Error("unsupported isolation level was inlined")
	}
}

func TestScopedEntryPointsRefuseMissingTenant(t *testing.T) {
	var p *Pool // 缺租户必须在碰连接之前就拒绝，nil 池证明没有发出任何语句
	ctx := context.Background()
	if err := p.InTx(ctx, Scope{}, func(pgx.Tx) error { return nil }); !errors.Is(err, errMissingTenant) {
		t.Fatalf("InTx without tenant = %v", err)
	}
	if err := p.InTxSerializable(ctx, Scope{}, func(pgx.Tx) error { return nil }); !errors.Is(err, errMissingTenant) {
		t.Fatalf("InTxSerializable without tenant = %v", err)
	}
	var v int
	if err := p.QueryRowScoped(ctx, Scope{}, `SELECT 1`, nil, &v); !errors.Is(err, errMissingTenant) {
		t.Fatalf("QueryRowScoped without tenant = %v", err)
	}
	if err := p.BatchScoped(ctx, Scope{}, BatchOptions{AsyncCommit: true}, &pgx.Batch{}); !errors.Is(err, errMissingTenant) {
		t.Fatalf("BatchScoped without tenant = %v", err)
	}
	if err := p.QueryScoped(ctx, Scope{}, `SELECT 1`, nil, func(pgx.Rows) error { return nil }); !errors.Is(err, errMissingTenant) {
		t.Fatalf("QueryScoped without tenant = %v", err)
	}
}

// 注入语句本身也守住事务级：第三个参数是 true。
func TestScopeSetConfigIsTransactionLocal(t *testing.T) {
	if strings.Count(scopeSetConfigSQL, ", true)") != 2 || strings.Contains(scopeSetConfigSQL, "false") {
		t.Fatalf("scope injection must be transaction-local: %s", scopeSetConfigSQL)
	}
	// 异步提交的注入也全是事务级：批次结束即失效，不会把 synchronous_commit=off 带回池里
	if strings.Count(scopeSetConfigAsyncSQL, ", true)") != 3 || strings.Contains(scopeSetConfigAsyncSQL, "false") ||
		!strings.Contains(scopeSetConfigAsyncSQL, "set_config('synchronous_commit', 'off', true)") {
		t.Fatalf("async scope injection must be transaction-local: %s", scopeSetConfigAsyncSQL)
	}
}
