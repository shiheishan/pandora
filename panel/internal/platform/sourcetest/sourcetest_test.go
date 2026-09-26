// [INPUT]: 依赖本包 Load 与 testdata/fixture 假包
// [OUTPUT]: 对外提供 TestDeclReturnsExactSourceWithoutDocComment（含 DeclWithDoc）、TestSourceCoversEveryNonTestFileRegardlessOfBuildTags、TestLookupFailsLoudly
// [POS]: platform/sourcetest 的自测：取声明的原文精确、整包源码不漏构建约束文件也不含测试文件、名字缺失或重名一定让测试失败
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package sourcetest

import (
	"fmt"
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
