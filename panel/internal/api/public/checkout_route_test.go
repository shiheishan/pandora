// [INPUT]: 依赖 platform/sourcetest 按名取 handlers.createOrder 的源码
// [OUTPUT]: 对外提供 TestCreateOrderHandlerOwnsClaimAndPreparedResponse
// [POS]: api/public 下单处理器自己消费幂等认领并写出预制响应，不重新编码
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCreateOrderHandlerOwnsClaimAndPreparedResponse(t *testing.T) {
	window := sourcetest.Load(t, ".").Decl("handlers.createOrder")
	for _, needle := range []string{
		"middleware.IdempotencyClaimFrom(r.Context())",
		"middleware.ValidateIdempotencyClaim(",
		"billing.CheckoutIdempotencyScope",
		"Claim:      claim",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(window, needle) {
			t.Errorf("createOrder handler missing %q", needle)
		}
	}
	if strings.Contains(window, "httpx.Created(") {
		t.Fatal("createOrder must not re-encode the completed idempotent response")
	}
}
