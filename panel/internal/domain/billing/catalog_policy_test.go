// [INPUT]: 依赖 catalogGroupAllowed、catalogPriceCurrentlyValid，依赖 platform/sourcetest 按名取 Service.CreateOrder 与 Service.CreateRenewal 的源码
// [OUTPUT]: 对外提供 TestCatalogGroupAuthorization、TestPurchaseSQLRestrictsCatalogCurrencies、TestCatalogPriceValidityWindow
// [POS]: billing 购买目录的授权与价格窗口：用户组白名单、价格生效区间（止于边界不含）、新购与续费的 SQL 只收 CNY / USD
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCatalogGroupAuthorization(t *testing.T) {
	a, b := "group-a", "group-b"
	if !catalogGroupAllowed(&a, []string{a, b}) {
		t.Fatal("authorized group rejected")
	}
	if catalogGroupAllowed(&a, []string{b}) {
		t.Fatal("unauthorized group accepted")
	}
	if catalogGroupAllowed(nil, []string{a}) {
		t.Fatal("user without a group accepted")
	}
}

func TestPurchaseSQLRestrictsCatalogCurrencies(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for _, name := range []string{"Service.CreateOrder", "Service.CreateRenewal"} {
		if !strings.Contains(pkg.Decl(name), "currency IN ('CNY','USD')") {
			t.Fatalf("%s lacks currency allowlist", name)
		}
	}
}

func TestCatalogPriceValidityWindow(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Minute)
	if !catalogPriceCurrentlyValid(&past, &future, now) {
		t.Fatal("active window rejected")
	}
	if catalogPriceCurrentlyValid(&future, nil, now) {
		t.Fatal("future price accepted")
	}
	if catalogPriceCurrentlyValid(nil, &now, now) {
		t.Fatal("expired price accepted at exclusive boundary")
	}
}
