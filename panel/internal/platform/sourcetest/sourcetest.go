// [INPUT]: 依赖 go/parser、go/ast 解析一个目录下的全部非测试 .go 源文件
// [OUTPUT]: 对外提供 Package、Load、Ref、TopDecl，以及 Package 的 Source、Decl、DeclWithDoc、Decls、FuncDecl、Refs、TopDecls
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
	"strconv"
	"strings"
	"testing"
)

// Package 是一个目录下全部非测试 .go 源文件（不看构建约束）的解析结果。
type Package struct {
	t      testing.TB
	dir    string
	source string
	decls  map[string][]decl
	files  []parsedFile
}

type parsedFile struct {
	name string // 文件名，不含目录
	fset *token.FileSet
	ast  *ast.File
}

type decl struct {
	file   string
	text   string
	doc    string // 文档注释原文（到声明起点为止），没有则为空
	node   ast.Decl
	syntax ast.Node // text 对应的语法树：函数即 node，分组声明是各自那一项
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
		p.files = append(p.files, parsedFile{name: filepath.Base(path), fset: fset, ast: file})
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
		p.decls[name] = append(p.decls[name], decl{file: filepath.Base(path), text: text(n), doc: docText, node: d, syntax: n})
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

// Ref 是一处对导入包成员的引用。
type Ref struct {
	File string // 文件名，不含目录
	Line int
	Name string // 写成「导入路径.成员」，如 os.Getenv
}

// Refs 返回全部非测试源文件里对 importPath 包中 names 成员的引用：调用与取函数值
// 都算，按每个文件自己的导入名解析别名。点导入无法按选择子识别，遇到直接失败，
// 不给静默漏报留余地。
func (p *Package) Refs(importPath string, names ...string) []Ref {
	p.t.Helper()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var refs []Ref
	for _, f := range p.files {
		local := ""
		for _, imp := range f.ast.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path != importPath {
				continue
			}
			local = filepath.Base(importPath)
			if imp.Name != nil {
				local = imp.Name.Name
			}
		}
		switch local {
		case "", "_":
			continue
		case ".":
			p.t.Fatalf("sourcetest: %s dot-imports %s; Refs cannot see its members", f.name, importPath)
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !want[sel.Sel.Name] {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
				refs = append(refs, Ref{File: f.name, Line: f.fset.Position(sel.Pos()).Line,
					Name: importPath + "." + sel.Sel.Name})
			}
			return true
		})
	}
	return refs
}

// TopDecl 是一个有名字的顶层声明，给逐声明扫描的守卫用（例如「哪些函数直接写响应体」，
// 白名单按「文件 + 声明名」登记）。
type TopDecl struct {
	File string // 文件名，不含目录
	Name string // 与 Decl 同一写法：函数名、接收者.方法、类型 / 变量 / 常量名
	// Node 是声明的语法树：函数为 *ast.FuncDecl；单个 var / const / type 为整句，
	// 分组声明为各自那一项（一项声明多个名字时每个名字各出现一次，Node 相同）
	Node ast.Node
	// Imports 是所在文件的导入名 → 导入路径（别名按别名），_ 导入不在里面
	Imports map[string]string
}

// TopDecls 按文件名、再按源码位置返回全部有名字的顶层声明；var _ 这种无名声明不在其中。
// 遇到点导入直接失败：选择子消失后按导入名识别调用的守卫会静默漏报。
func (p *Package) TopDecls() []TopDecl {
	p.t.Helper()
	imports := map[string]map[string]string{}
	for _, f := range p.files {
		m := map[string]string{}
		for _, imp := range f.ast.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			local := filepath.Base(path)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			switch local {
			case "_":
				continue
			case ".":
				p.t.Fatalf("sourcetest: %s dot-imports %s; TopDecls cannot attribute its members", f.name, path)
			}
			m[local] = path
		}
		imports[f.name] = m
	}
	var out []TopDecl
	for name, found := range p.decls {
		for _, d := range found {
			out = append(out, TopDecl{File: d.file, Name: name, Node: d.syntax, Imports: imports[d.file]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Node.Pos() != out[j].Node.Pos() {
			return out[i].Node.Pos() < out[j].Node.Pos()
		}
		return out[i].Name < out[j].Name
	})
	return out
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
