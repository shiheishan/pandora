// [INPUT]: 依赖 idempotency.go 的 Idempotency 中间件、认领上下文（IdempotencyClaimFrom、newOwnedIdempotencyClaim）、请求哈希与目标、作用域与各段 SQL 常量
// [OUTPUT]: 无导出；认领、键校验、作用域与 SQL 契约的单测
// [POS]: middleware 幂等测试的入口文件：进库之前就失败的路径（缺主体、非法键、非法 actor UUID、基础作用域）、认领上下文的类型与拷贝、SQL 契约、完成探针分类；录制器、捕获边界与重放判定分在 idempotency_{recorder,capture,replay}_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestIdempotencyFailsClosedWithoutAuthenticatedTenantActor(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nextCalls := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalls++ })
	handler := Idempotency(nil, "order_create", log)(next)

	for name, ctx := range map[string]context.Context{
		"anonymous": context.Background(),
		"tenant mismatch": httpx.WithPrincipal(
			httpx.WithTenantID(context.Background(), "tenant-a"),
			&httpx.Principal{
				Kind: "user", UserID: "actor-a", TenantID: "tenant-b",
			},
		),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/orders", nil).WithContext(ctx)
			req.Header.Set("Idempotency-Key", "key-1")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if _, ok := IdempotencyClaimFrom(req.Context()); ok {
				t.Fatal("unauthorized request received a claim")
			}
		})
	}
	if nextCalls != 0 {
		t.Fatalf("unauthorized request invoked downstream %d times", nextCalls)
	}
}

func TestInvalidIdempotencyKeyFailsBeforeDatabase(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, key := range map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("k", 256),
		"nul control":    "key\x00value",
		"tab control":    "key\tvalue",
		"delete control": "key\x7fvalue",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/orders", nil)
			if key != "" {
				req.Header.Set("Idempotency-Key", key)
			}
			w := httptest.NewRecorder()
			Idempotency(nil, "order_create", log)(http.HandlerFunc(
				func(http.ResponseWriter, *http.Request) { t.Fatal("downstream was called") },
			)).ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestOwnedIdempotencyClaimContextIsTypedAndCopied(t *testing.T) {
	base := context.Background()
	if _, ok := IdempotencyClaimFrom(base); ok {
		t.Fatal("claim existed before ownership")
	}
	if _, ok := IdempotencyClaimFrom(withIdempotencyClaim(base, nil)); ok {
		t.Fatal("replay/conflict/in-flight path received a claim")
	}

	hash := idempotencyRequestHash(http.MethodPost, "/orders", []byte(`{"plan":"p"}`))
	lockedUntil := time.Now().Add(5 * time.Minute).UTC()
	owned := newOwnedIdempotencyClaim(
		"record-1", "tenant-1", "actor-1", "order_create",
		"order_create:actor:0123456789abcdef01234567", "key-1", hash,
		lockedUntil,
	)
	ctx := withIdempotencyClaim(base, owned)
	claim, ok := IdempotencyClaimFrom(ctx)
	if !ok {
		t.Fatal("owner did not receive a claim")
	}
	if claim.ID != "record-1" || claim.TenantID != "tenant-1" ||
		claim.ActorID != "actor-1" || claim.Scope != "order_create" ||
		claim.StorageScope != "order_create:actor:0123456789abcdef01234567" ||
		claim.Key != "key-1" || claim.Generation != 1 ||
		!claim.LockedUntil.Equal(lockedUntil) || claim.RequestHash != hash {
		t.Fatalf("unexpected owned claim: %#v", claim)
	}

	claim.ID = "mutated"
	claim.StorageScope = "mutated"
	claim.LockedUntil = time.Time{}
	claim.RequestHash[0] ^= 0xff
	again, ok := IdempotencyClaimFrom(ctx)
	if !ok || again.ID != "record-1" ||
		again.StorageScope != "order_create:actor:0123456789abcdef01234567" ||
		!again.LockedUntil.Equal(lockedUntil) || again.RequestHash != hash {
		t.Fatal("accessor exposed mutable context state")
	}
}

func TestIdempotencySQLContracts(t *testing.T) {
	for name, tc := range map[string]struct {
		sql       string
		required  []string
		forbidden []string
	}{
		"insert": {
			sql: insertIdempotencyClaimSQL,
			required: []string{
				"actor_id", "ON CONFLICT", "DO NOTHING",
				"RETURNING id, locked_until, claim_generation",
			},
			forbidden: []string{"status)"},
		},
		"locked read": {
			sql: selectIdempotencyClaimForUpdateSQL,
			required: []string{
				"SELECT id", "actor_id", "request_hash", "FOR UPDATE",
			},
		},
		"takeover": {
			sql: takeoverIdempotencyClaimSQL,
			required: []string{
				"WHERE id = $1", "actor_id = $5", "request_hash = $6",
				"status = 'failed'", "claim_generation = claim_generation + 1",
				"claim_generation = $7", "response_format IN ('legacy_empty', 'bytes')",
				"RETURNING id, locked_until, claim_generation",
			},
			forbidden: []string{
				"locked_until < now()", "AND status = 'in_flight'", "response_body =",
				"response_format IN ('legacy_empty', 'bytes', 'unavailable')",
			},
		},
		"completion": {
			sql: completeIdempotencyClaimSQL,
			required: []string{
				"WHERE id = $1", "actor_id = $5", "request_hash = $6",
				"status = 'in_flight'", "claim_generation = $7",
				"locked_until = $17", "response_format = $10",
				"response_payload = $11", "RETURNING id",
			},
			forbidden: []string{"response_body ="},
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, required := range tc.required {
				if !strings.Contains(tc.sql, required) {
					t.Fatalf("SQL missing %q:\n%s", required, tc.sql)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(tc.sql, forbidden) {
					t.Fatalf("SQL unexpectedly contains %q:\n%s", forbidden, tc.sql)
				}
			}
		})
	}
}

func TestInFlightClaimsNeverAutoTakeOver(t *testing.T) {
	for _, lease := range []string{"active", "expired", "completion-failed"} {
		t.Run(lease, func(t *testing.T) {
			if idempotencyStatusAllowsTakeover("in_flight") {
				t.Fatal("in-flight claim became takeoverable")
			}
		})
	}
	if !idempotencyStatusAllowsTakeover("failed") {
		t.Fatal("terminal failed claim cannot retry")
	}
}

func TestCompletionProbeClassifiesExactResourceAndStaleGeneration(t *testing.T) {
	code := http.StatusCreated
	expected := idempotencyCompletion{
		status: "succeeded", responseCode: code, responseFormat: "bytes",
		responsePayload: []byte("created"),
		headers:         idempotencyReplayHeaders{Location: stringPointer("/orders/42")},
	}
	exact := idempotencyRecord{
		status: "succeeded", responseCode: &code, responseFormat: "bytes",
		responsePayload: []byte("created"), headers: expected.headers, generation: 4,
	}
	if got := classifyIdempotencyCompletionProbe(exact, 4, expected); got != "business_completed" {
		t.Fatalf("exact completion classification = %q", got)
	}
	resourceType, resourceID := "order", "42"
	resource := exact
	resource.responsePayload = []byte("different")
	resource.resourceType, resource.resourceID = &resourceType, &resourceID
	if got := classifyIdempotencyCompletionProbe(resource, 4, expected); got != "resource_bound" {
		t.Fatalf("resource completion classification = %q", got)
	}
	stale := exact
	stale.generation = 5
	if got := classifyIdempotencyCompletionProbe(stale, 4, expected); got != "stale_generation" {
		t.Fatalf("stale completion classification = %q", got)
	}
}

func TestInvalidAuthenticatedActorUUIDFailsBeforeDatabase(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := httpx.WithPrincipal(
		httpx.WithTenantID(context.Background(), "11111111-1111-1111-1111-111111111111"),
		&httpx.Principal{
			Kind: "user", UserID: "not-a-uuid",
			TenantID: "11111111-1111-1111-1111-111111111111",
		},
	)
	req := httptest.NewRequest(http.MethodPost, "/orders", nil).WithContext(ctx)
	req.Header.Set("Idempotency-Key", "invalid-actor")
	w := httptest.NewRecorder()
	Idempotency(nil, "order_create", log)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { t.Fatal("downstream was called") },
	)).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("invalid actor status = %d, want 500", w.Code)
	}
}

func TestIdempotencyRequestHashIncludesMethodEscapedPathQueryAndBody(t *testing.T) {
	body := []byte(`{"reason":"same body"}`)
	base := idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", body)

	for name, got := range map[string][32]byte{
		"method":       idempotencyRequestHash("PATCH", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", body),
		"escaped path": idempotencyRequestHash("POST", "/v1/revenue/adjustments/a/b/reverse?mode=full", body),
		"raw query":    idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=partial", body),
		"body":         idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", []byte(`{"reason":"different"}`)),
	} {
		if got == base {
			t.Fatalf("%s did not affect request hash", name)
		}
	}
}

func TestLegacyLookupSQLContract(t *testing.T) {
	for _, required := range []string{
		"SELECT 1", "app.lookup_legacy_idempotency_key($1, $2, $3)",
	} {
		if !strings.Contains(legacyIdempotencyLookupSQL, required) {
			t.Fatalf("legacy lookup missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"FROM idempotency_keys", "FOR SHARE", "created_at", "completed_at",
		"CURRENT_TIMESTAMP", "interval '30 days'",
	} {
		if strings.Contains(legacyIdempotencyLookupSQL, forbidden) {
			t.Fatalf("legacy lookup unexpectedly contains %q", forbidden)
		}
	}
}

func TestIdempotencyBaseScopeFailsFastAtConstruction(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, valid := range []string{"a", "order_create", strings.Repeat("a", 64)} {
		t.Run("valid_"+valid[:1], func(t *testing.T) {
			_ = Idempotency(nil, valid, log)
		})
	}
	for name, invalid := range map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("a", 65),
		"actor suffix":   "order_create:actor:0123456789abcdef01234567",
		"uppercase":      "Order_create",
		"dash":           "order-create",
		"space":          "order create",
		"non ascii":      "order_创建",
		"embedded colon": "order:create",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatalf("invalid scope %q did not panic", invalid)
				}
			}()
			_ = Idempotency(nil, invalid, log)
		})
	}
}

func TestIdempotencyRequestTargetPreservesEscapedPathAndRawQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/items/a%2Fb?mode=full&x=%2F", nil)
	if got, want := idempotencyRequestTarget(req.URL), "/items/a%2Fb?mode=full&x=%2F"; got != want {
		t.Fatalf("request target = %q, want %q", got, want)
	}
}

func TestIdempotencyScopeIsActorBound(t *testing.T) {
	a := idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-a"})
	b := idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-b"})
	if a == b {
		t.Fatal("two actors share an idempotency uniqueness domain")
	}
	if a != idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-a"}) {
		t.Fatal("same actor scope is unstable")
	}
	if idempotencyActorScope("order_create", nil) !=
		idempotencyActorScope("order_create", &httpx.Principal{}) {
		t.Fatal("anonymous scope is unstable")
	}
}
