package nodefabric

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// 设备识别窗口（R103）只有一个出处：迁移 00094 的 app.device_limit_window_minutes。
// Go 侧的可选值必须与它认的值一致，清理截止必须不小于最大窗口，
// 后台节点列表的在线统计不能再写死一个窗口字面量。
func TestDeviceWindowHasOneSource(t *testing.T) {
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "00094_device_window_minutes.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.SplitN(string(migration), "-- +goose Down", 2)[0]
	for _, m := range DeviceWindowMinutes {
		v := strconv.Itoa(m)
		if !strings.Contains(up, "WHEN '"+v+"' THEN "+v) {
			t.Errorf("app.device_limit_window_minutes does not accept %d", m)
		}
	}
	if strings.Count(up, "WHEN '") != len(DeviceWindowMinutes) {
		t.Errorf("migration accepts values Go does not know: %v", DeviceWindowMinutes)
	}
	if !strings.Contains(up, "), 5)") || DeviceWindowMinutes[0] != 5 {
		t.Error("missing or invalid window must fall back to 5 minutes")
	}
	if !strings.Contains(up, "make_interval(mins => app.device_limit_window_minutes(tenant_id))") {
		t.Error("subscription_online_devices must read the tenant window")
	}

	if max := slices.Max(DeviceWindowMinutes[:]); staleAliveRetentionMinutes <= max {
		t.Fatalf("PurgeStaleAlive cutoff %d min would delete rows inside the %d min window",
			staleAliveRetentionMinutes, max)
	}

	handlers, err := os.ReadFile(filepath.Join("..", "..", "api", "admin", "handlers.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(handlers), "app.device_limit_window_minutes($1)") {
		t.Error("admin node list online stats must use the tenant device window")
	}
	for _, file := range []string{"uniproxy.go", filepath.Join("..", "..", "api", "admin", "handlers.go")} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "last_seen_at > now() - interval") {
			t.Errorf("%s hard-codes an online window literal", file)
		}
	}
}
