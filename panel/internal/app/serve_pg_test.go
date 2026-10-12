package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"go.uber.org/zap"
)

// 后台不分权限，登录检查是唯一的门：后台引擎上除登录、续期、登出和探活外，每条路由匿名访问都必须 401。
// 新模块漏挂 RequireAuth 时这里会红。要连库（写开关闸门读库），只在 CI 跑。
func TestAdminRoutesRequireLogin(t *testing.T) {
	d := testkit.DB(t)
	_, rdb := testkit.Redis(t)
	r := &runtime{cfg: testConfig(t), log: zap.NewNop(), db: d, redis: rdb}
	svc, err := buildServices(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.close)
	_, adminEng := newEngines(r, svc)

	open := map[string]bool{
		"POST /v1/auth/login":   true,
		"POST /v1/auth/refresh": true,
		"POST /v1/auth/logout":  true,
		"GET /healthz":          true,
		"GET /readyz":           true,
	}
	checked := 0
	for _, ri := range adminEng.Routes() {
		key := ri.Method + " " + ri.Path
		if open[key] {
			continue
		}
		path := fillParams(ri.Path)
		req := httptest.NewRequest(ri.Method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		adminEng.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s 匿名访问得 %d，应 401：%s", key, w.Code, w.Body.String())
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("只检查了 %d 条后台路由，路由表不对", checked)
	}
}

// fillParams 把路由模板里的 :id、*path 换成可用的值。
func fillParams(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if strings.HasPrefix(s, ":") || strings.HasPrefix(s, "*") {
			parts[i] = "00000000-0000-4000-8000-000000000001"
		}
	}
	return strings.Join(parts, "/")
}
