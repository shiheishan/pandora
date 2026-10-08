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
	// 新购的价格读取在 checkout_catalog.go，续费的在 renewal_price.go（都是报价与建单共用）
	for _, name := range []string{"loadNewPurchasePriceTx", "listPlanPricesTx", "loadRenewalPriceTx"} {
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
