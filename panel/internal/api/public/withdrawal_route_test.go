// [INPUT]: 依赖 domain/billing 的幂等域常量，依赖 platform/sourcetest 按名取 NewRouter 的源码
// [OUTPUT]: 对外提供 TestWithdrawalRouteRequiresIdempotency
// [POS]: api/public 提现申请挂独立幂等域，与转余额不共用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 提现申请动的是钱：与转余额一样要求幂等键，重试拿回同一个结果（契约 7.3）。
func TestWithdrawalRouteRequiresIdempotency(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("NewRouter")
	idx := strings.Index(src, `Post("/me/withdrawals", h.requestWithdrawal)`)
	if idx < 0 {
		t.Fatal("withdrawal route missing or not wrapped by r.With")
	}
	start := idx - 200
	if start < 0 {
		start = 0
	}
	window := src[start:idx]
	if !strings.Contains(window, "middleware.Idempotency(d.Pool, billing.CommissionWithdrawalIdempotencyScope") {
		t.Fatalf("withdrawal route lacks its idempotency middleware:\n%s", window)
	}
	if billing.CommissionWithdrawalIdempotencyScope == billing.CommissionTransferIdempotencyScope {
		t.Fatal("withdrawal and transfer must not share an idempotency scope")
	}
}
