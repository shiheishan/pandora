package adminops

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// 缺陷 12：向导编辑必须是一个事务。此前资料、版本、价格各走各的事务，
// 后一步失败时前面已提交的改动留在库里。
func TestUpdatePlanCompleteRunsInOneTransaction(t *testing.T) {
	body, err := os.ReadFile("plan_wizard_update.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, "func (s *Service) UpdatePlanComplete(")
	end := strings.Index(src, "// quotaDiffers")
	if start < 0 || end <= start {
		t.Fatal("could not isolate UpdatePlanComplete")
	}
	orchestration := src[start:end]
	if n := strings.Count(orchestration, "s.pool.InTx("); n != 1 {
		t.Fatalf("UpdatePlanComplete opens %d transactions, want exactly 1", n)
	}
	// 自带事务的公开用例不能在编排里被调用，否则又会各自提交。
	for _, ownTx := range []string{
		"s.GetPlan(", "s.UpdatePlan(", "s.CreatePlanVersion(", "s.UpdatePlanVersion(",
		"s.PublishPlanVersion(", "s.CreatePlanPrice(", "s.bindPoolsToFreshVersion(",
	} {
		if strings.Contains(src[start:], ownTx) {
			t.Errorf("wizard update calls self-committing %s", ownTx)
		}
	}
	for _, step := range []string{
		"FOR UPDATE", "loadPlanTx(", "s.updatePlanTx(", "syncPlanPricesTx(", "s.rollPlanVersionTx(",
	} {
		if !strings.Contains(orchestration, step) {
			t.Errorf("wizard update orchestration missing %s", step)
		}
	}
}

func TestPriceSyncScopeIsSubmittedCurrenciesPublicOffersOnly(t *testing.T) {
	wanted, err := preparePlanPrices([]PlanPriceInput{
		{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1000, Currency: "CNY"},
		{BillingInterval: "year", IntervalCount: 1, UnitAmount: 9000, Currency: "CNY"},
	}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if got := priceSyncCurrencies(wanted); !slices.Equal(got, []string{"CNY"}) {
		t.Fatalf("sync currencies=%v, want only the submitted CNY", got)
	}
	if got := priceSyncCurrencies(nil); len(got) != 0 {
		t.Fatalf("empty submission must touch no currency, got %v", got)
	}

	body, err := os.ReadFile("plan_wizard_update.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	sync := src[strings.Index(src, "func syncPlanPricesTx("):strings.Index(src, "// rollPlanVersionTx")]
	for _, scope := range []string{"user_group_id IS NULL", "currency::text = ANY($3::text[])"} {
		if !strings.Contains(sync, scope) {
			t.Errorf("price sync must be limited by %q", scope)
		}
	}
}

func TestPreparePlanPricesRejectsInvalidTierBeforeTransaction(t *testing.T) {
	if _, err := preparePlanPrices([]PlanPriceInput{
		{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1000, Currency: "EUR"},
	}, "actor"); err == nil {
		t.Fatal("unsupported currency must be rejected before any write")
	}
}
