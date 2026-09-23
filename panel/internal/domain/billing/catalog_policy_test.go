package billing

import (
	"os"
	"strings"
	"testing"
	"time"
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
	for _, name := range []string{"checkout.go", "renewal.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "currency IN ('CNY','USD')") {
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
