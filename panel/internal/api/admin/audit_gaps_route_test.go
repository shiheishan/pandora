package admin

import (
	"reflect"
	"testing"
)

// 审计补漏（w5account，审计台账 2.3）：改 Telegram 设置要近期重认证；批量生成的结果下载
// 带明文初始口令，同样要近期重认证；看进度只要写权限。
func TestAuditGapRouteProtection(t *testing.T) {
	routes := loadRouteProtections(t)
	for key, want := range map[string]routeProtection{
		"POST /settings/telegram": {handler: "h.setTelegramSettings",
			permissions: []string{"platform.settings.write"}, recentReauth: true},
		"GET /users/bulk/generate/jobs/{id}/result": {handler: "h.downloadUserGenerationResult",
			permissions: []string{"iam.user.write"}, recentReauth: true},
		"GET /users/bulk/generate/jobs/{id}": {handler: "h.getUserGenerationJob",
			permissions: []string{"iam.user.write"}},
		"GET /users/bulk/generate/jobs": {handler: "h.listUserGenerationJobs",
			permissions: []string{"iam.user.write"}},
	} {
		if got, ok := routes[key]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v (registered=%v), want %+v", key, got, ok, want)
		}
	}
}
