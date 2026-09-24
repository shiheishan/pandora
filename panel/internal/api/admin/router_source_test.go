// [INPUT]: 依赖本包 router.go 与 router_<模块>.go 的源码，依赖 go/parser
// [OUTPUT]: 对外提供 routerSourceFiles、routerSource、inspectRouterFiles 三个测试辅助
// [POS]: api/admin 源码级路由契约测试的公共入口：路由表拆成多文件后，契约测试经它读全部路由源码，而不是只读 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// routerSourceFiles 返回路由表的全部源文件：router.go 在前，其余 router_*.go 按名排序。
func routerSourceFiles() ([]string, error) {
	matches, err := filepath.Glob("router_*.go")
	if err != nil {
		return nil, err
	}
	files := []string{"router.go"}
	for _, name := range matches {
		if !strings.HasSuffix(name, "_test.go") {
			files = append(files, name)
		}
	}
	sort.Strings(files[1:])
	return files, nil
}

// routerSource 把全部路由源文件首尾相接，给按文本查找的契约测试用，
// 签名与 os.ReadFile 一致。同一段路由只会在一个文件里，窗口式断言不受拼接影响。
func routerSource() ([]byte, error) {
	files, err := routerSourceFiles()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out.Write(body)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// inspectRouterFiles 对每个路由源文件的 AST 调用 ast.Inspect。
func inspectRouterFiles(t *testing.T, visit func(ast.Node) bool) {
	t.Helper()
	for _, file := range parseRouterFiles(t) {
		ast.Inspect(file, visit)
	}
}

func parseRouterFiles(t *testing.T) []*ast.File {
	t.Helper()
	names, err := routerSourceFiles()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(names))
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return files
}
