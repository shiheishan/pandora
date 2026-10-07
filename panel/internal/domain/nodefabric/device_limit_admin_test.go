package nodefabric

import (
	"reflect"
	"strings"
	"testing"
)

// 后台在线设备概览：在线数按订阅走 LATERAL（条件推进视图，不把全站在线记录聚合一遍），
// 每行带订阅主人的 user_id（前端凭它直接打开用户抽屉）。
func TestOnlineDevicesOverviewReadsPerSubscription(t *testing.T) {
	for _, want := range []string{
		"LEFT JOIN LATERAL (",
		"WHERE od.tenant_id = s.tenant_id AND od.subscription_id = s.id",
		"s.user_id::text",
	} {
		if !strings.Contains(onlineDevicesSQL, want) {
			t.Fatalf("online devices overview SQL missing %q", want)
		}
	}
	field, ok := reflect.TypeOf(OnlineDevice{}).FieldByName("UserID")
	if !ok || field.Tag.Get("json") != "user_id" {
		t.Fatal("OnlineDevice must expose user_id")
	}
}
