package billing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCommissionDefaultsMatchSeed(t *testing.T) {
	read := func(file string) string {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	// 00028 建行（费率 0、冻结 7、最低提现 1000），00029 把后两项改成实际要用的
	// 3 天与 10000——生效的种子是 00029 的值。
	seed28, seed29 := read("00028_commission_settings.sql"), read("00029_commission_defaults.sql")
	for _, literal := range []string{
		fmt.Sprintf(`('commission.rate_percent', '%d'::jsonb`, CommissionDefaultRatePercent),
	} {
		if !strings.Contains(seed28, literal) {
			t.Errorf("00028 no longer seeds %s", literal)
		}
	}
	for _, literal := range []string{
		fmt.Sprintf(`SET value = '%d'::jsonb, updated_at = now()
 WHERE key = 'commission.freeze_days'`, CommissionDefaultFreezeDays),
		fmt.Sprintf(`SET value = '%d'::jsonb, updated_at = now()
 WHERE key = 'commission.min_withdraw'`, CommissionDefaultMinWithdraw),
	} {
		if !strings.Contains(seed29, literal) {
			t.Errorf("00029 no longer sets the default %q", literal)
		}
	}

	// 两个读取点都用这组常量，不再各写一份字面量
	for name, src := range map[string]string{
		"loadCommissionConfig":    sourcetest.Load(t, ".").Decl("loadCommissionConfig"),
		"AdminCommissionOverview": sourcetest.Load(t, ".").Decl("Service.AdminCommissionOverview"),
	} {
		for _, want := range []string{"CommissionDefaultRatePercent", "CommissionDefaultFreezeDays",
			"CommissionDefaultMinWithdraw"} {
			if !strings.Contains(src, want) {
				t.Errorf("%s must fall back to %s", name, want)
			}
		}
		for _, literal := range []string{"'commission.freeze_days'), 3)", "'commission.freeze_days'),3)",
			"'commission.min_withdraw'), 10000)", "'commission.min_withdraw'),10000)"} {
			if strings.Contains(src, literal) {
				t.Errorf("%s restates a literal fallback %q", name, literal)
			}
		}
	}
}
