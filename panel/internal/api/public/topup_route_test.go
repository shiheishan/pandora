// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter 与 handlers.createTopup 的源码
// [OUTPUT]: 对外提供 TestTopupRouteHasIndependentIdempotencyMiddleware、TestTopupHandlerWritesTransactionPreparedResponse
// [POS]: api/public 充值的独立幂等域与事务内预制响应
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestTopupRouteHasIndependentIdempotencyMiddleware(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("NewRouter")
	idx := strings.Index(src, "/me/topups")
	if idx < 0 {
		t.Fatal("topup route missing")
	}
	start := idx - 220
	if start < 0 {
		start = 0
	}
	window := src[start:idx]
	if !strings.Contains(window, "middleware.Idempotency") ||
		!strings.Contains(window, "billing.TopupIdempotencyScope") {
		t.Fatal("topup route lacks its independent idempotency middleware")
	}
}

func TestTopupHandlerWritesTransactionPreparedResponse(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("handlers.createTopup")
	for _, required := range []string{
		"middleware.IdempotencyClaimFrom",
		"middleware.ValidateIdempotencyClaim",
		"httpx.WritePrepared",
		"out.PreparedResponse()",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("topup handler missing %s", required)
		}
	}
	if strings.Contains(body, "httpx.OK") || strings.Contains(body, "map[string]any") {
		t.Fatal("topup handler re-encodes or rebuilds its committed response")
	}
}
