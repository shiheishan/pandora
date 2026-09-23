// [INPUT]: 依赖本包非测试源码里的 RequirePermission 声明与 migrations/*.sql 里 INSERT INTO permissions 的权限字典
// [OUTPUT]: 对外提供 TestRoutePermissionsExistInCatalog 契约测试
// [POS]: admin 网关的权限字典守卫：00010_seed_rbac.sql 头部承诺“路由声明的权限码必须都在字典里”，运行时并没有那个启动核对，这里用源码契约测试落实它
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var (
	requirePermissionPattern = regexp.MustCompile(`RequirePermission\("([^"]+)"`)
	permissionInsertPattern  = regexp.MustCompile(`(?s)INSERT INTO permissions.*?;`)
	permissionCodePattern    = regexp.MustCompile(`'([a-z]+(?:\.[a-z_]+)+)'`)
)

func TestRoutePermissionsExistInCatalog(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test source")
	}
	packageDir := filepath.Dir(sourceFile)
	root := filepath.Clean(filepath.Join(packageDir, "..", "..", ".."))

	catalog := map[string]bool{}
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	for _, file := range migrations {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, statement := range permissionInsertPattern.FindAllString(string(body), -1) {
			for _, match := range permissionCodePattern.FindAllStringSubmatch(statement, -1) {
				catalog[match[1]] = true
			}
		}
	}
	if !catalog["iam.user.read"] {
		t.Fatal("permission catalog did not parse: iam.user.read missing")
	}

	declared := map[string]bool{}
	sources, err := filepath.Glob(filepath.Join(packageDir, "*.go"))
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	for _, file := range sources {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, match := range requirePermissionPattern.FindAllStringSubmatch(string(body), -1) {
			declared[match[1]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("no RequirePermission declarations found in package admin")
	}

	var missing []string
	for code := range declared {
		if !catalog[code] {
			missing = append(missing, code)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("路由声明了权限字典里不存在的权限码：%s", strings.Join(missing, ", "))
	}
}
