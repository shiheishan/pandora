package dbbackup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkpointHookRoot 只能在声明处取值：同包测试要把它指向临时目录，所以它是 var 不是 const；
// 非测试代码里任何对它的赋值、自增自减、取地址都会让「Go 与 install.sh 建的目录相同」那条守卫
// （deploy-single-layout_static_test.sh ⑤）落空。按语法树找，局部同名变量（:= 或函数参数）不算
func TestCheckpointHookRootIsNeverReassigned(t *testing.T) {
	found, declared := hookRootWrites(t, ".")
	if !declared {
		t.Fatal("package-level checkpointHookRoot declaration not found: the scan looked at the wrong place")
	}
	if len(found) > 0 {
		t.Fatalf("checkpointHookRoot is written outside its declaration: %v", found)
	}
}

// 守卫自己要抓得到：在临时包里放几种写法，必须全部认出；只读的用法不算
func TestHookRootWriteScannerCatchesWrites(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package dbbackup\n\nimport \"path/filepath\"\n\nvar _ = filepath.Clean\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("decl.go", "var checkpointHookRoot = \"/opt/pandora/checkpoint-sink\"\n")
	write("reads.go", `func reads(p string) bool {
	if rel, err := filepath.Rel(checkpointHookRoot, p); err != nil || rel == "" {
		return checkpointHookRoot != p
	}
	checkpointHookRoot := "local shadow"
	checkpointHookRoot = "still the local one"
	return checkpointHookRoot == ""
}
`)
	// 空目录：找不到声明，必须报出来（守卫看错了目录时不能悄悄通过）
	if _, declared := hookRootWrites(t, t.TempDir()); declared {
		t.Fatal("an empty directory reported the declaration as found")
	}
	if found, declared := hookRootWrites(t, dir); len(found) != 0 || !declared {
		t.Fatalf("reads and a local shadow were taken as writes (declared=%v): %v", declared, found)
	}
	write("init.go", "func init() { checkpointHookRoot = \"/tmp\" }\n")
	write("paren.go", "func f() { (checkpointHookRoot) = \"/tmp\" }\n")
	write("multi.go", "func g() (string, error) { var err error; checkpointHookRoot, err = \"/tmp\", nil; return \"\", err }\n")
	write("addr.go", "func h() *string { return &checkpointHookRoot }\n")
	write("op.go", "func k() { checkpointHookRoot += \"/x\" }\n")
	write("range.go", "func r(xs []string) { for _, checkpointHookRoot = range xs {} }\n")
	write("rangekey.go", "func rk(m map[string]int) { for checkpointHookRoot = range m {} }\n")
	write("rangeshadow.go", "func rs(xs []string) { for _, checkpointHookRoot := range xs { _ = checkpointHookRoot } }\n")
	if found, _ := hookRootWrites(t, dir); len(found) != 7 {
		t.Fatalf("expected 7 writes (init, paren, multi, addr, op, range value, range key), found %d: %v", len(found), found)
	}
}

// hookRootWrites 返回目录里非测试 Go 文件对包级 checkpointHookRoot 的写入位置，以及是否找到了它的包级声明
// （找不到说明扫错了地方，调用方要当失败）
func hookRootWrites(t *testing.T, dir string) ([]string, bool) {
	t.Helper()
	fset := token.NewFileSet()
	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	declared := false
	for _, name := range matches {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range file.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.VAR {
				for _, sp := range g.Specs {
					for _, n := range sp.(*ast.ValueSpec).Names {
						declared = declared || n.Name == "checkpointHookRoot"
					}
				}
			}
		}
		// 包级的 checkpointHookRoot：单文件解析时，别的文件里声明的包级名字解析不到（Obj 为 nil）；
		// 本文件里的包级声明 Obj 指向顶层 ValueSpec。局部变量的 Obj 指向函数里的声明
		pkgLevel := func(e ast.Expr) bool {
			for {
				p, ok := e.(*ast.ParenExpr)
				if !ok {
					break
				}
				e = p.X
			}
			id, ok := e.(*ast.Ident)
			if !ok || id.Name != "checkpointHookRoot" {
				return false
			}
			if id.Obj == nil {
				return true
			}
			for _, d := range file.Decls {
				if g, ok := d.(*ast.GenDecl); ok {
					for _, s := range g.Specs {
						if s == id.Obj.Decl {
							return true
						}
					}
				}
			}
			return false
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				if v.Tok != token.DEFINE {
					for _, l := range v.Lhs {
						if pkgLevel(l) {
							found = append(found, fset.Position(l.Pos()).String())
						}
					}
				}
			case *ast.RangeStmt:
				// for checkpointHookRoot = range …、for _, checkpointHookRoot = range …（:= 是新的局部变量）
				if v.Tok == token.ASSIGN {
					for _, e := range []ast.Expr{v.Key, v.Value} {
						if e != nil && pkgLevel(e) {
							found = append(found, fset.Position(e.Pos()).String())
						}
					}
				}
			case *ast.IncDecStmt:
				if pkgLevel(v.X) {
					found = append(found, fset.Position(v.Pos()).String())
				}
			case *ast.UnaryExpr:
				if v.Op == token.AND && pkgLevel(v.X) {
					found = append(found, fset.Position(v.Pos()).String())
				}
			}
			return true
		})
	}
	return found, declared
}
