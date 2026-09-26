// [INPUT]: 依赖 usage_daily.go 的 UsageLocation / UsageDay
// [OUTPUT]: 对外提供 TestUsageLocationFallsBack、TestUsageDayCutsAtLocalMidnight
// [POS]: domain/nodefabric 按日流量日界口径的单元测试，与 usage_daily_pg18_test.go 的真实 SQL 门禁互补
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"testing"
	"time"
)

func TestUsageLocationFallsBack(t *testing.T) {
	for _, tc := range []struct {
		user, tenant, want string
	}{
		{"America/New_York", "Asia/Shanghai", "America/New_York"},
		{"Not/AZone", "Asia/Shanghai", "Asia/Shanghai"},
		// 空串与 Local 在 Go 里是 UTC 与服务器本地时区，都不能算用户选的时区。
		{"", "Asia/Shanghai", "Asia/Shanghai"},
		{"Local", "Asia/Shanghai", "Asia/Shanghai"},
		{"Not/AZone", "Also/Bogus", "UTC"},
		{"", "", "UTC"},
		// R50：用户 'UTC' 是从没设过的默认值，跟随站点时区；站点本身是 UTC 时仍按 UTC
		{"UTC", "Asia/Shanghai", "Asia/Shanghai"},
		{"UTC", "UTC", "UTC"},
		{"UTC", "", "UTC"},
	} {
		if got := UsageLocation(tc.user, tc.tenant).String(); got != tc.want {
			t.Errorf("UsageLocation(%q, %q) = %q, want %q", tc.user, tc.tenant, got, tc.want)
		}
	}
}

func TestUsageDayCutsAtLocalMidnight(t *testing.T) {
	// 17:30 UTC 在上海已是次日 01:30，在纽约还是当天 13:30。
	at := time.Date(2026, 9, 24, 17, 30, 0, 0, time.UTC)
	for _, tc := range []struct {
		zone, want string
	}{
		{"Asia/Shanghai", "2026-09-25"},
		{"America/New_York", "2026-09-24"},
		{"UTC", "2026-09-24"},
	} {
		day := UsageDay(at, UsageLocation(tc.zone, ""))
		if got := day.Format(time.DateOnly); got != tc.want {
			t.Errorf("UsageDay in %s = %s, want %s", tc.zone, got, tc.want)
		}
		// 结果钉在 UTC 零点：pgx 按 date 编码只取年月日，不能再被时区偏一天。
		if day.Location() != time.UTC || day.Hour() != 0 || day.Minute() != 0 {
			t.Errorf("UsageDay in %s = %v, want UTC midnight", tc.zone, day)
		}
	}
}
