// [INPUT]: 依赖本包 compareFiles、shatterFile
// [OUTPUT]: 对外提供 TestCompareAcceptsPureMove、TestCompareRejectsBehaviourChanges、TestShatterIsAPureMove
// [POS]: tools/refactorcheck 的自测：纯挪动（换文件、换顺序、换导入块、约束写法互换）判相同，改体、改文档注释、换约束、换导入路径、改名判不同；shatter 的输出经 compare 判为纯挪动
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const fixtureA = `// 文件头注释可以变
package p

import (
	"fmt"
	str "strings"
)

//==============================================================================
// 分节注释是游离注释，只提示不判错
//==============================================================================

// Greet 的文档注释跟着声明走
func Greet(name string) string {
	// 函数体里的注释也算原文
	return fmt.Sprintf("hi %s", str.ToUpper(name))
}

type Box[T any] struct{ v T }

func (b *Box[T]) Get() T { return b.v }

const (
	First  = "first"
	Second = "second"
)

func init() {}
`

const fixtureLinux = `package p

func OnlyLinux() string { return "linux" }

func init() {}
`

func files(pairs ...string) []srcFile {
	var out []srcFile
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, srcFile{name: pairs[i], src: []byte(pairs[i+1])})
	}
	return out
}

func mustCompare(t *testing.T, base, head []srcFile) compareResult {
	t.Helper()
	r, err := compareFiles(base, head)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCompareAcceptsPureMove(t *testing.T) {
	base := files("a.go", fixtureA, "x_linux.go", fixtureLinux)
	head := files(
		"box.go", `package p

func (b *Box[T]) Get() T { return b.v }

type Box[T any] struct{ v T }
`,
		"greet.go", `// 新文件的头注释
package p

import (
	str "strings"
	"fmt"
)

func init() {}

// Greet 的文档注释跟着声明走
func Greet(name string) string {
	// 函数体里的注释也算原文
	return fmt.Sprintf("hi %s", str.ToUpper(name))
}

const (
	First  = "first"
	Second = "second"
)
`,
		// _linux.go 后缀换成 //go:build linux，真值表相同
		"moved.go", `//go:build linux

package p

func init() {}

func OnlyLinux() string { return "linux" }
`)
	r := mustCompare(t, base, head)
	if !r.ok() {
		t.Fatalf("pure move rejected: %+v", r)
	}
	if r.baseDecls != 7 || r.headDecls != 7 {
		t.Fatalf("decl counts base=%d head=%d, want 7", r.baseDecls, r.headDecls)
	}
	if len(r.floatingBase) != 1 || !strings.Contains(r.floatingBase[0], "分节注释") {
		t.Fatalf("section comment not reported as floating: %q", r.floatingBase)
	}
}

func TestCompareRejectsBehaviourChanges(t *testing.T) {
	base := files("a.go", fixtureA, "x_linux.go", fixtureLinux)
	for name, tc := range map[string]struct {
		head []srcFile
		key  string
		kind string
	}{
		"body changed": {
			head: files("a.go", strings.Replace(fixtureA, `"hi %s"`, `"hello %s"`, 1), "x_linux.go", fixtureLinux),
			key:  "p func Greet", kind: "changed",
		},
		"doc comment changed": {
			head: files("a.go", strings.Replace(fixtureA, "跟着声明走", "被改了", 1), "x_linux.go", fixtureLinux),
			key:  "p func Greet", kind: "changed",
		},
		"linux-only decl moved into an unconstrained file": {
			head: files("a.go", fixtureA+"\nfunc OnlyLinux() string { return \"linux\" }\n",
				"x_linux.go", "package p\n\nfunc init() {}\n"),
			key: "p func OnlyLinux", kind: "changed",
		},
		"same alias, different import path": {
			head: files("a.go", strings.Replace(fixtureA, `str "strings"`, `str "bytes"`, 1), "x_linux.go", fixtureLinux),
			key:  "p func Greet", kind: "changed",
		},
		"renamed": {
			head: files("a.go", strings.Replace(fixtureA, "func Greet(", "func Hello(", 1), "x_linux.go", fixtureLinux),
			key:  "p func Greet", kind: "onlyBase",
		},
		"duplicate init dropped": {
			head: files("a.go", strings.Replace(fixtureA, "func init() {}\n", "", 1), "x_linux.go", fixtureLinux),
			key:  "p func init", kind: "changed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := mustCompare(t, base, tc.head)
			var got []string
			switch tc.kind {
			case "changed":
				got = r.changed
			case "onlyBase":
				got = r.onlyBase
			}
			if r.ok() || len(got) != 1 || got[0] != tc.key {
				t.Fatalf("want %s %q, got %+v", tc.kind, tc.key, r)
			}
		})
	}
}

func TestShatterIsAPureMove(t *testing.T) {
	blank := `//go:build linux

// Package p 的包注释
package p

import (
	_ "embed"
	"fmt"
)

//go:embed a.txt
var blob string

func Show() string { return fmt.Sprint(blob) }

// 结尾的游离注释
`
	for name, src := range map[string]string{"a.go": fixtureA, "x_linux.go": fixtureLinux, "embed.go": blank} {
		t.Run(name, func(t *testing.T) {
			original := srcFile{name: name, src: []byte(src)}
			chunks, err := shatterFile(original, map[string]string{"strings": "strings", "fmt": "fmt"})
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range chunks {
				if _, err := parser.ParseFile(token.NewFileSet(), c.name, c.src, parser.ParseComments); err != nil {
					t.Fatalf("chunk %s does not parse: %v\n%s", c.name, err, c.src)
				}
				if name == "x_linux.go" && !strings.HasSuffix(c.name, "_linux.go") {
					t.Fatalf("chunk %s lost the _linux suffix", c.name)
				}
			}
			r := mustCompare(t, []srcFile{original}, chunks)
			if !r.ok() || r.baseDecls != len(chunks) {
				t.Fatalf("shatter is not a pure move: %+v", r)
			}
		})
	}
}
