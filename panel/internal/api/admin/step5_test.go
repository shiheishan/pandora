// [INPUT]: 依赖 finance_routes_contract_test.go 的 loadRouteProtections（读全部 router*.go 的 AST），依赖 site_settings.go 的 validSiteTimezone
// [OUTPUT]: 对外提供 TestStep5RouteProtections、TestValidSiteTimezone
// [POS]: api/admin 第 ⑤ 步新增与改动路由的保护契约（权限码、重认证、幂等域逐条钉死）与处理器纯函数单测
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"reflect"
	"testing"
)

func TestStep5RouteProtections(t *testing.T) {
	routes := loadRouteProtections(t)
	for key, want := range map[string]routeProtection{
		// 降级开关：每次切换都要重认证（契约后台-09），不带幂等
		"POST /switches/{code}": {handler: "h.setSwitch",
			permissions: []string{"platform.settings.write"}, recentReauth: true},
		// 手动重置直接改变可用额度：权限 → 重认证 → 幂等
		"POST /users/{id}/traffic-reset": {handler: "h.manualResetTraffic",
			permissions: []string{"metering.reset.write"}, recentReauth: true, idempotency: "traffic_manual_reset"},
		// 全局出站与分流：读挂 node.read；发布影响全部节点，重认证 + 幂等
		"GET /nodes/routing": {handler: "h.nodeGetGlobalRouting", permissions: []string{"node.read"}},
		"PUT /nodes/routing": {handler: "h.nodeSetGlobalRouting", permissions: []string{"node.config.publish"},
			recentReauth: true, idempotency: "node_routing_global_publish"},
		"POST /nodes/{id}/retire": {handler: "h.nodeRetire", permissions: []string{"node.lifecycle"},
			recentReauth: true, idempotency: "node_retire"},
		// 站点时区（R49）：读与邮件设置同权，写改变全部按日统计的切日口径
		"GET /settings/site": {handler: "h.getSiteSettings", permissions: []string{"security.audit.read"}},
		"POST /settings/site": {handler: "h.setSiteSettings",
			permissions: []string{"platform.settings.write"}, recentReauth: true},
	} {
		if got, ok := routes[key]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v (registered=%v), want %+v", key, got, ok, want)
		}
	}
}

func TestValidSiteTimezone(t *testing.T) {
	for name, want := range map[string]bool{
		"Asia/Shanghai": true, "UTC": true, "America/New_York": true,
		"": false, "Local": false, "Not/AZone": false, "../../etc/passwd": false,
		// 大小写写错的名字（asia/shanghai）在 Linux 上加载失败而被拒；macOS 的文件系统
		// 不分大小写会放行，所以不放进这张表
	} {
		if got := validSiteTimezone(name); got != want {
			t.Errorf("validSiteTimezone(%q) = %v, want %v", name, got, want)
		}
	}
}
