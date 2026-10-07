package nodefabric

import (
	"reflect"
	"strings"
	"testing"
)

// 后台在线设备概览：先把窗口内的在线记录按订阅聚合一次、取前 200，最后才拼用户与套餐；
// 不再对每条在用订阅走一次在线设备视图的 LATERAL（5k 在用订阅时 p50 171ms）。
// 每行带订阅主人的 user_id（前端凭它直接打开用户抽屉）。
func TestOnlineDevicesOverviewAggregatesBeforeJoining(t *testing.T) {
	sql := onlineDevicesSQL
	for _, want := range []string{
		"GROUP BY x.subscription_id",
		"(SELECT app.device_limit_window_minutes($1))",
		"AND NOT EXISTS (SELECT 1 FROM online o WHERE o.subscription_id = s.id)",
		"page.user_id::text",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("online devices overview SQL missing %q", want)
		}
	}
	for _, banned := range []string{"LATERAL", "subscription_online_devices"} {
		if strings.Contains(sql, banned) {
			t.Fatalf("online devices overview SQL must not use %q per subscription", banned)
		}
	}
	// 取前 200 在拼用户与套餐之前
	page := strings.Index(sql, "), page AS (")
	limit := strings.LastIndex(sql, "LIMIT 200")
	join := strings.Index(sql, "LEFT JOIN users u")
	if page < 0 || limit < page || join < limit {
		t.Fatal("online devices overview must cut the page to 200 rows before joining users and plans")
	}
	field, ok := reflect.TypeOf(OnlineDevice{}).FieldByName("UserID")
	if !ok || field.Tag.Get("json") != "user_id" {
		t.Fatal("OnlineDevice must expose user_id")
	}
}
