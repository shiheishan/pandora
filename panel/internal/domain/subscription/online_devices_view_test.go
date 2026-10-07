package subscription

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 在线设备视图的现行定义（最后一个重定义它的迁移的 Up 段）必须：
//   - 窗口只从 app.device_limit_window_minutes 来（R103，没有分钟字面量）；
//   - 当前租户的窗口用不相关子查询求一次，不逐行调函数；
//   - 按订阅取数（条件落在 idx_node_alive_recent 上），没有顶层 GROUP BY——
//     顶层聚合会挡住调用方 JOIN 条件的下推，只查一条订阅也要聚合全表（00098 修的就是它）。
func TestOnlineDevicesViewReadsTenantWindow(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	latest, body := "", ""
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		up := strings.SplitN(string(raw), "-- +goose Down", 2)[0]
		if i := strings.Index(up, "CREATE OR REPLACE VIEW subscription_online_devices"); i >= 0 {
			latest, body = name, up[i:]
		}
	}
	if latest == "" {
		t.Fatal("no migration defines subscription_online_devices")
	}
	// 先去掉行内注释（注释里提到 GROUP BY、分号都不算），再截到语句结尾
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if at := strings.Index(line, "--"); at >= 0 {
			lines[i] = line[:at]
		}
	}
	body = strings.Join(lines, "\n")
	if end := strings.Index(body, ";"); end >= 0 {
		body = body[:end]
	}
	for _, want := range []string{
		"(SELECT app.device_limit_window_minutes(app.current_tenant_id()))",
		"x.subscription_id = s.id",
		"x.last_seen_at > now() - make_interval(mins =>",
		"count(DISTINCT x.ip_hash)",
		"count(DISTINCT x.node_id)",
		"HAVING count(*) > 0",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("%s: subscription_online_devices lacks %q", latest, want)
		}
	}
	if strings.Contains(body, "interval '") {
		t.Fatalf("%s: online window must come from app.device_limit_window_minutes, not a literal", latest)
	}
	// 顶层 GROUP BY 会让视图退回「先聚合全表再 JOIN」
	if strings.Contains(strings.ToUpper(body), "GROUP BY") {
		t.Fatalf("%s: subscription_online_devices must aggregate per subscription, not GROUP BY the whole table", latest)
	}
}
