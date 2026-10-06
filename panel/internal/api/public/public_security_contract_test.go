// [INPUT]: 依赖 platform/sourcetest 按名取 handlers.telegramUpdate、handlers.subscribe 与 NewRouter 的源码及全包声明
// [OUTPUT]: 对外提供 TestTelegramWebhookValidatesPersistedSecret、TestCommissionTransferRequiresIdempotencyMiddleware、TestSubscribeTakesClientIPFromHTTPX
// [POS]: api/public 的三条安全契约：Telegram 回调常量时间比对持久化密钥、佣金转余额挂幂等、订阅分发的来源地址只经 httpx.ClientIP

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

// 订阅分发记下的来源地址与限流、审计同一口径：只认 nginx 覆写的 X-Real-IP。
// 这里曾有一份自写的 clientIP，X-Real-IP 缺失时取 X-Forwarded-For 最左值——那个值
// 客户端想填什么就填什么。httpx.ClientIP 本身不读 XFF 由 httpx 的单测守着。
func TestSubscribeTakesClientIPFromHTTPX(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	if !strings.Contains(pkg.Decl("handlers.subscribe"), "ip := httpx.ClientIP(r)") {
		t.Fatal("subscribe must take the client IP from httpx.ClientIP")
	}
	// 只看字符串字面量（带引号），注释里提到这个头不算
	if strings.Contains(strings.ToLower(pkg.Source()), `"x-forwarded-for"`) {
		t.Fatal("api/public must not parse X-Forwarded-For; use httpx.ClientIP")
	}
	for _, d := range pkg.TopDecls() {
		if strings.EqualFold(d.Name, "clientIP") {
			t.Fatalf("api/public declares its own %s in %s; use httpx.ClientIP", d.Name, d.File)
		}
	}
}
