package sourcetest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

// fatalTB 把 Fatalf 变成可捕获的 panic，用来断言查找失败时确实会让测试失败。
type fatalTB struct {
	testing.TB
}

type fatalMessage string

func (fatalTB) Helper() {}

func (fatalTB) Fatalf(format string, args ...any) {
	panic(fatalMessage(fmt.Sprintf(format, args...)))
}

func expectFatal(t *testing.T, want string, fn func(tb testing.TB)) {
	t.Helper()
	defer func() {
		t.Helper()
		msg, ok := recover().(fatalMessage)
		if !ok || !strings.Contains(string(msg), want) {
			t.Fatalf("fatal message = %q, want it to mention %q", msg, want)
		}
	}()
	fn(fatalTB{t})
}

func TestDeclReturnsExactSourceWithoutDocComment(t *testing.T) {
	p := Load(t, "testdata/fixture")
	for name, want := range map[string]string{
		"Plain":   `func Plain() string { return "plain" }`,
		"Box.Get": `func (b *Box[T]) Get() T { return b.v }`,
		"Box":     `type Box[T any] struct{ v T }`,
		"Second":  `Second = "second"`,
		"Single":  `var Single = "single"`,
	} {
		if got := p.Decl(name); got != want {
			t.Errorf("Decl(%q) = %q, want %q", name, got, want)
		}
	}
	if got, want := p.Decls("Plain", "Single"), p.Decl("Plain")+"\n"+p.Decl("Single"); got != want {
		t.Errorf("Decls = %q, want %q", got, want)
	}
	if got, want := p.DeclWithDoc("Plain"), "// Plain 的文档注释不进 Decl 的结果\n"+p.Decl("Plain"); got != want {
		t.Errorf("DeclWithDoc(Plain) = %q, want %q", got, want)
	}
	if got := p.DeclWithDoc("Single"); got != p.Decl("Single") {
		t.Errorf("DeclWithDoc without a doc comment = %q, want the bare declaration", got)
	}
	if fn := p.FuncDecl("Box.Get"); fn.Name.Name != "Get" {
		t.Errorf("FuncDecl(Box.Get) = %s", fn.Name.Name)
	}
}

func TestSourceCoversEveryNonTestFileRegardlessOfBuildTags(t *testing.T) {
	src := Load(t, "testdata/fixture").Source()
	for _, want := range []string{`return "plain"`, `return "linux"`, `return "other"`} {
		if !strings.Contains(src, want) {
			t.Errorf("package source missing %q", want)
		}
	}
	if strings.Contains(src, "TestOnly") {
		t.Error("package source must not include _test.go files")
	}
}

func TestLookupFailsLoudly(t *testing.T) {
	expectFatal(t, "not found", func(tb testing.TB) { Load(tb, "testdata/fixture").Decl("Missing") })
	expectFatal(t, "not found", func(tb testing.TB) { Load(tb, "testdata/fixture").Decl("TestOnly") })
	expectFatal(t, "ambiguous", func(tb testing.TB) { Load(tb, "testdata/fixture").Decl("Twin") })
	expectFatal(t, "not a function", func(tb testing.TB) { Load(tb, "testdata/fixture").FuncDecl("Box") })
	expectFatal(t, "no non-test Go files", func(tb testing.TB) { Load(tb, "testdata") })
}

func TestRefsResolvesImportAliasesAndFunctionValues(t *testing.T) {
	refs := Load(t, "testdata/fixture").Refs("os", "Getenv", "LookupEnv")
	if len(refs) != 2 {
		t.Fatalf("Refs = %+v, want the call and the function value in d.go", refs)
	}
	for _, r := range refs {
		if r.File != "d.go" || r.Line != 7 {
			t.Errorf("unexpected ref %+v", r)
		}
	}
	if got := Load(t, "testdata/fixture").Refs("os", "Environ"); len(got) != 0 {
		t.Errorf("Refs(Environ) = %+v, want none", got)
	}
	expectFatal(t, "dot-imports", func(tb testing.TB) { Load(tb, "testdata/dotimport").Refs("os", "Getenv") })
}

func TestTopDeclsListsEveryNamedDeclWithItsFileAndImports(t *testing.T) {
	var got []string
	for _, d := range Load(t, "testdata/fixture").TopDecls() {
		got = append(got, d.File+" "+d.Name)
		if d.Name == "Aliased" {
			if _, ok := d.Node.(*ast.FuncDecl); !ok || d.Imports["osx"] != "os" {
				t.Errorf("Aliased: node %T imports %v, want *ast.FuncDecl with osx → os", d.Node, d.Imports)
			}
		}
		if d.Name == "First" {
			if _, ok := d.Node.(*ast.ValueSpec); !ok {
				t.Errorf("First: node %T, want the grouped *ast.ValueSpec", d.Node)
			}
		}
	}
	// 文件名有序、文件内按位置；_test.go 里的 TestOnly 不在其中，两份 Twin 各算一次
	want := "a.go Plain|a.go Box|a.go Box.Get|a.go First|a.go Second|a.go Single|"
	if joined := strings.Join(got, "|"); !strings.HasPrefix(joined, want) ||
		strings.Contains(joined, "TestOnly") || strings.Count(joined, " Twin") != 2 || !strings.Contains(joined, "d.go Aliased") {
		t.Fatalf("TopDecls = %s", joined)
	}
	expectFatal(t, "dot-imports", func(tb testing.TB) { Load(tb, "testdata/dotimport").TopDecls() })
}
