// [INPUT]: 依赖 go/parser、go/ast 解析一个目录下的全部非测试 .go 源文件
// [OUTPUT]: 对外提供 Package、Load，以及 Package 的 Source、Decl、DeclWithDoc、Decls、FuncDecl
// [POS]: platform 的测试辅助包：源码契约测试按「包 + 声明名」取源码，而不是按文件名读，函数在包内换文件不影响断言；只被 *_test.go 引用，不进任何生产二进制
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package sourcetest 给源码契约测试按声明名取源码。
//
// 这类测试断言「某函数里必须有 / 不许有某段代码」。过去它们按文件名读源码、
// 再用「从函数 A 到函数 B」的文本窗口截出函数体，函数一换文件就红，否定断言
// 更糟：目标函数挪走后会静默通过。这里按包加载全部非测试源码，按名字取顶层
// 声明的原文（不含文档注释，保留原始排版，字面量断言照常匹配），名字找不到或
// 有重名时直接失败，不给静默通过留余地。
//
// 名字写法：函数写函数名；方法写「接收者类型.方法名」，不带星号与类型参数
// （Service.Login、handlers.createOrder）；类型、变量、常量写标识符本身。
package sourcetest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Package 是一个目录下全部非测试 .go 源文件（不看构建约束）的解析结果。
type Package struct {
	t      testing.TB
	dir    string
	source string
	decls  map[string][]decl
}

type decl struct {
	file string
	text string
	doc  string // 文档注释原文（到声明起点为止），没有则为空
	node ast.Decl
}

// Load 解析 dir 下全部非测试 .go 文件；目录读不了、没有源文件或解析失败都直接失败。
func Load(t testing.TB, dir string) *Package {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("sourcetest: list %s: %v", dir, err)
	}
	p := &Package{t: t, dir: dir, decls: map[string][]decl{}}
	var all strings.Builder
	fset := token.NewFileSet()
	sort.Strings(paths)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("sourcetest: read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("sourcetest: parse %s: %v", path, err)
		}
		all.Write(src)
		all.WriteByte('\n')
		for _, d := range file.Decls {
			p.index(fset, path, src, d)
		}
	}
	if all.Len() == 0 {
		t.Fatalf("sourcetest: no non-test Go files in %s", dir)
	}
	p.source = all.String()
	return p
}

func (p *Package) index(fset *token.FileSet, path string, src []byte, d ast.Decl) {
	text := func(n ast.Node) string {
		return string(src[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
	}
	add := func(name string, n ast.Node, doc *ast.CommentGroup) {
		if name == "_" {
			return
		}
		var docText string
		if doc != nil {
			docText = string(src[fset.Position(doc.Pos()).Offset:fset.Position(n.Pos()).Offset])
		}
		p.decls[name] = append(p.decls[name], decl{file: filepath.Base(path), text: text(n), doc: docText, node: d})
	}
	switch x := d.(type) {
	case *ast.FuncDecl:
		add(funcName(x), x, x.Doc)
	case *ast.GenDecl:
		// 单个声明取整句（带 type / var / const 关键字），分组声明取各自那一项
		for _, spec := range x.Specs {
			var n ast.Node = spec
			doc := x.Doc
			if len(x.Specs) == 1 {
				n = x
			} else {
				doc = nil
			}
			switch s := spec.(type) {
			case *ast.TypeSpec:
				if doc == nil {
					doc = s.Doc
				}
				add(s.Name.Name, n, doc)
			case *ast.ValueSpec:
				if doc == nil {
					doc = s.Doc
				}
				for _, id := range s.Names {
					add(id.Name, n, doc)
				}
			}
		}
	}
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	switch generic := typ.(type) {
	case *ast.IndexExpr:
		typ = generic.X
	case *ast.IndexListExpr:
		typ = generic.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// Source 返回全部非测试源文件按文件名排序后首尾相接的原文，给「整个包里都不许有」
// 这类断言用。包内的先后顺序没有意义，不要在它上面比较位置。
func (p *Package) Source() string {
	return p.source
}

// Decl 返回名为 name 的顶层声明的原文，不存在或重名（例如分属不同构建约束的
// 两个文件）时失败。
func (p *Package) Decl(name string) string {
	p.t.Helper()
	return p.lookup(name).text
}

// DeclWithDoc 与 Decl 相同，但连同紧贴其上的文档注释一起返回，给断言落在
// 文档注释里的契约用（例如注释里写明的锁序）。
func (p *Package) DeclWithDoc(name string) string {
	p.t.Helper()
	found := p.lookup(name)
	return found.doc + found.text
}

// Decls 按参数顺序拼接多个顶层声明的原文，中间隔一个换行。给跨越几个声明的
// 断言用：参数顺序即拼接顺序，比较位置时位置只在这个拼接里有意义。
func (p *Package) Decls(names ...string) string {
	p.t.Helper()
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = p.lookup(name).text
	}
	return strings.Join(parts, "\n")
}

// FuncDecl 返回名为 name 的函数或方法的语法树，给要走 AST 的断言用。
func (p *Package) FuncDecl(name string) *ast.FuncDecl {
	p.t.Helper()
	fn, ok := p.lookup(name).node.(*ast.FuncDecl)
	if !ok {
		p.t.Fatalf("sourcetest: %s in %s is not a function", name, p.dir)
	}
	return fn
}

func (p *Package) lookup(name string) decl {
	p.t.Helper()
	found := p.decls[name]
	switch len(found) {
	case 0:
		p.t.Fatalf("sourcetest: declaration %s not found in %s", name, p.dir)
	case 1:
		return found[0]
	}
	files := make([]string, len(found))
	for i, d := range found {
		files[i] = d.file
	}
	p.t.Fatalf("sourcetest: declaration %s is ambiguous in %s (%s)", name, p.dir, strings.Join(files, ", "))
	return decl{}
}
