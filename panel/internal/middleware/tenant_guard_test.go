// [INPUT]: 依赖 middleware.go 的 Tenant 与 DefaultTenantID，依赖 platform/sourcetest 与 platform/httpx；扫描 panel 的 internal、cmd 非测试 Go 源码与 deploy 下非 test-* 的 shell 脚本
// [OUTPUT]: 对外提供 TestSingleTenantAssumptionGuard
// [POS]: middleware 的守卫单测：产品当前只有一个租户，新租户只有 app.seed_tenant_defaults 种下的行；一旦出现建租户的产品代码，或网关不再恒定注入默认租户，就逼人先把种子补全（⑩）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

const tenantSeedHint = "多租户前先扩 app.seed_tenant_defaults，至少补系统角色与权限（新租户缺角色会建不出管理员）"

var insertTenants = regexp.MustCompile(`(?i)INSERT\s+INTO\s+(public\.)?tenants\b`)

// 建租户触发器（00090）只种渠道、模板与开关，不种角色、主题与其余设置——多租户
// 本身不做，产品也没有建租户的入口。这条守卫让「开始建第二个租户」这件事不可能
// 悄悄发生：要么有代码往 tenants 里插行，要么网关按请求解析租户，两者都会让测试红。
func TestSingleTenantAssumptionGuard(t *testing.T) {
	var offenders []string
	scan := func(root string, want func(path string) bool) {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !want(path) {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if insertTenants.Match(body) {
				offenders = append(offenders, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
	goSource := func(path string) bool {
		return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
	}
	scan(filepath.Join("..", "..", "internal"), goSource)
	scan(filepath.Join("..", "..", "cmd"), goSource)
	scan(filepath.Join("..", "..", "deploy"), func(path string) bool {
		return strings.HasSuffix(path, ".sh") && !strings.HasPrefix(filepath.Base(path), "test-")
	})
	if len(offenders) > 0 {
		t.Fatalf("product code now inserts tenants (%s): %s", strings.Join(offenders, ", "), tenantSeedHint)
	}

	// 网关恒定注入默认租户：源码与行为各钉一次
	if !strings.Contains(sourcetest.Load(t, ".").Decl("Tenant"),
		"httpx.WithTenantID(r.Context(), DefaultTenantID)") {
		t.Fatalf("middleware.Tenant no longer pins DefaultTenantID: %s", tenantSeedHint)
	}
	var got string
	Tenant(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = httpx.TenantIDFrom(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got != DefaultTenantID {
		t.Fatalf("middleware.Tenant injected %q, want %s: %s", got, DefaultTenantID, tenantSeedHint)
	}
}
