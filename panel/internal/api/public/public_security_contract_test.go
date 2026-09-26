// [INPUT]: 依赖 platform/sourcetest 按名取 handlers.telegramUpdate 与 NewRouter 的源码
// [OUTPUT]: 对外提供 TestTelegramWebhookValidatesPersistedSecret、TestCommissionTransferRequiresIdempotencyMiddleware
// [POS]: api/public 的两条安全契约：Telegram 回调常量时间比对持久化密钥、佣金转余额挂幂等
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestTelegramWebhookValidatesPersistedSecret(t *testing.T) {
	handler := sourcetest.Load(t, ".").Decl("handlers.telegramUpdate")
	for _, want := range []string{
		`chi.URLParam(r, "secret")`,
		"subtle.ConstantTimeCompare",
		"WebhookSecret",
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("telegram webhook handler lacks secret validation contract %q", want)
		}
	}
}

func TestCommissionTransferRequiresIdempotencyMiddleware(t *testing.T) {
	source := sourcetest.Load(t, ".").Decl("NewRouter")
	route := `Post("/me/commission/transfer", h.transferCommission)`
	at := strings.Index(source, route)
	if at < 0 {
		t.Fatal("commission transfer route missing")
	}
	start := at - 300
	if start < 0 {
		start = 0
	}
	window := source[start:at]
	if !strings.Contains(window, "middleware.Idempotency") ||
		!strings.Contains(window, "billing.CommissionTransferIdempotencyScope") {
		t.Fatal("commission transfer route lacks globally unique idempotency middleware scope")
	}
}
