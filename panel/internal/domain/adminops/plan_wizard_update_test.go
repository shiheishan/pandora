// [INPUT]: 依赖 preparePlanPrices、priceSyncCurrencies，依赖 platform/sourcetest 按名取向导编辑及其辅助函数的源码
// [OUTPUT]: 对外提供 TestUpdatePlanCompleteRunsInOneTransaction、TestPriceSyncScopeIsSubmittedCurrenciesPublicOffersOnly、TestPreparePlanPricesRejectsInvalidTierBeforeTransaction
// [POS]: adminops 套餐向导编辑（缺陷 12）：一个事务、不调自带事务的公开用例、价格同步只动提交的币种的公开报价
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 缺陷 12：向导编辑必须是一个事务。此前资料、版本、价格各走各的事务，
// 后一步失败时前面已提交的改动留在库里。
func TestUpdatePlanCompleteRunsInOneTransaction(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	orchestration := pkg.Decls("Service.UpdatePlanComplete", "validateWizardQuotaEdits")
	// 向导编排及它用到的全部辅助函数
	wizard := pkg.Decls("Service.UpdatePlanComplete", "validateWizardQuotaEdits", "quotaDiffers",
		"currentVersion", "poolsDiffer", "boolOr", "sameIntPtr", "pricesKey", "preparePlanPrices",
		"priceSyncCurrencies", "syncPlanPricesTx", "Service.rollPlanVersionTx", "inheritVersionSemantics")
	if n := strings.Count(orchestration, "s.pool.InTx("); n != 1 {
		t.Fatalf("UpdatePlanComplete opens %d transactions, want exactly 1", n)
	}
	// 自带事务的公开用例不能在编排里被调用，否则又会各自提交。
	for _, ownTx := range []string{
		"s.GetPlan(", "s.UpdatePlan(", "s.CreatePlanVersion(", "s.UpdatePlanVersion(",
		"s.PublishPlanVersion(", "s.CreatePlanPrice(", "s.bindPoolsToFreshVersion(",
	} {
		if strings.Contains(wizard, ownTx) {
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

	sync := sourcetest.Load(t, ".").Decl("syncPlanPricesTx")
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
