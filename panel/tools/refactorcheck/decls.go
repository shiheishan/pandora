// [INPUT]: 依赖 go/parser、go/printer 解析与规范化打印，依赖 go/build/constraint 求构建约束
// [OUTPUT]: 对外提供 srcFile、pkgDecls、collectDecls、tagUniverse、buildTagsOf、importName
// [POS]: tools/refactorcheck 的声明指纹：把一个包的源文件折成「键 → 指纹」的多重集合，compare 比对它，shatter 的自测也用它证明自己是纯挪动
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// srcFile 是一个源文件：name 为基名，src 为原始字节。
type srcFile struct {
	name string
	src  []byte
}

// pkgDecls 是一个包（一侧版本）的全部顶层声明。
//
// 键是「包名 种类 名字」，值是指纹列表（init、var _ 之类可重名，按多重集合比）。
// 指纹 = 所在文件构建约束的真值表 + 声明用到的导入路径 + gofmt 后的原文（含文档注释）：
// 原文相同但挪进了约束不同的文件、或同一个本地名指向了另一个导入路径，都算行为变化。
type pkgDecls struct {
	decls    map[string][]string
	lines    map[string]int // 文件 → 行数
	floating []string       // 不属于任何声明的注释（分节标题之类），只作提示
}

var (
	knownOS   = splitSet("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
	knownArch = splitSet("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le " +
		"ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
	unixOS = splitSet("aix android darwin dragonfly freebsd hurd illumos ios linux netbsd openbsd solaris")
	// 求真值表用的代表平台：足以区分仓库里出现的全部约束
	evalOS   = []string{"linux", "darwin", "windows", "freebsd", "js"}
	evalArch = []string{"amd64", "arm64", "386", "arm", "wasm"}
)

func splitSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}

// tagUniverse 是两侧文件里出现过的全部自定义构建标签（非平台、非 go 版本），
// 真值表对它们的每种取值都求一遍，两侧必须用同一个 universe。
type tagUniverse []string

// fileConstraint 返回文件的构建约束：//go:build 表达式与文件名后缀隐含的平台，二者取与。
func fileConstraint(f srcFile) (constraint.Expr, error) {
	var exprs []constraint.Expr
	for _, line := range strings.Split(string(f.src), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if constraint.IsGoBuild(trimmed) {
			x, err := constraint.Parse(trimmed)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", f.name, err)
			}
			exprs = append(exprs, x)
		}
	}
	for _, tag := range fileNameTags(f.name) {
		exprs = append(exprs, &constraint.TagExpr{Tag: tag})
	}
	if len(exprs) == 0 {
		return nil, nil
	}
	x := exprs[0]
	for _, y := range exprs[1:] {
		x = &constraint.AndExpr{X: x, Y: y}
	}
	return x, nil
}

// fileNameTags 按 go/build 的规则从 name_GOOS_GOARCH.go 里取隐含的平台标签。
func fileNameTags(name string) []string {
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "_test")
	parts := strings.Split(name, "_")
	n := len(parts)
	if n >= 3 && knownOS[parts[n-2]] && knownArch[parts[n-1]] {
		return []string{parts[n-2], parts[n-1]}
	}
	if n >= 2 && (knownOS[parts[n-1]] || knownArch[parts[n-1]]) {
		return []string{parts[n-1]}
	}
	return nil
}

// buildTagsOf 收集文件里的自定义标签，供两侧合成同一个 tagUniverse。
func buildTagsOf(files []srcFile, into map[string]bool) error {
	for _, f := range files {
		x, err := fileConstraint(f)
		if err != nil {
			return err
		}
		walkTags(x, func(tag string) {
			if !knownOS[tag] && !knownArch[tag] && tag != "unix" && tag != "gc" && !strings.HasPrefix(tag, "go1.") {
				into[tag] = true
			}
		})
	}
	return nil
}

func walkTags(x constraint.Expr, fn func(string)) {
	switch e := x.(type) {
	case *constraint.TagExpr:
		fn(e.Tag)
	case *constraint.NotExpr:
		walkTags(e.X, fn)
	case *constraint.AndExpr:
		walkTags(e.X, fn)
		walkTags(e.Y, fn)
	case *constraint.OrExpr:
		walkTags(e.X, fn)
		walkTags(e.Y, fn)
	}
}

// truthTable 把约束化成与写法无关的真值表：//go:build linux 与 _linux.go 后缀得到同一个结果。
func truthTable(x constraint.Expr, universe tagUniverse) string {
	if x == nil {
		return "all"
	}
	var b strings.Builder
	all := true
	for _, goos := range evalOS {
		for _, goarch := range evalArch {
			for mask := 0; mask < 1<<len(universe); mask++ {
				on := func(tag string) bool {
					switch {
					case tag == goos || tag == goarch, tag == "gc", strings.HasPrefix(tag, "go1."):
						return true
					case tag == "unix":
						return unixOS[goos]
					}
					for i, u := range universe {
						if u == tag {
							return mask&(1<<i) != 0
						}
					}
					return false
				}
				if x.Eval(on) {
					b.WriteByte('1')
				} else {
					b.WriteByte('0')
					all = false
				}
			}
		}
	}
	if all {
		return "all"
	}
	return b.String()
}

// collectDecls 解析一个包的全部文件，得到声明指纹的多重集合。
func collectDecls(files []srcFile, universe tagUniverse) (*pkgDecls, error) {
	out := &pkgDecls{decls: map[string][]string{}, lines: map[string]int{}}
	for _, f := range files {
		out.lines[f.name] = bytes.Count(f.src, []byte("\n"))
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.name, f.src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		x, err := fileConstraint(f)
		if err != nil {
			return nil, err
		}
		table := truthTable(x, universe)
		imports := map[string]string{}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			local := importName(path, nil)
			if spec.Name != nil {
				local = spec.Name.Name
			}
			imports[local] = path
		}
		covered := map[*ast.CommentGroup]bool{}
		for _, d := range file.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				continue
			}
			start, end := declRange(d), d.End()
			var inner []*ast.CommentGroup
			for _, cg := range file.Comments {
				if cg.Pos() >= start && cg.End() <= end {
					inner = append(inner, cg)
					covered[cg] = true
				}
			}
			var text bytes.Buffer
			cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
			if err := cfg.Fprint(&text, fset, &printer.CommentedNode{Node: d, Comments: inner}); err != nil {
				return nil, err
			}
			key := file.Name.Name + " " + declKey(d)
			value := "build: " + table + "\nimports: " + usedImports(d, imports) + "\n" + text.String()
			out.decls[key] = append(out.decls[key], value)
		}
		for _, cg := range file.Comments {
			if !covered[cg] && cg.Pos() > file.Package {
				out.floating = append(out.floating, strings.TrimSpace(cg.Text()))
			}
		}
	}
	for _, values := range out.decls {
		sort.Strings(values)
	}
	sort.Strings(out.floating)
	return out, nil
}

// declRange 的起点包含文档注释：文档注释跟着声明走，丢了或改了都算差异。
func declRange(d ast.Decl) token.Pos {
	switch x := d.(type) {
	case *ast.FuncDecl:
		if x.Doc != nil {
			return x.Doc.Pos()
		}
	case *ast.GenDecl:
		if x.Doc != nil {
			return x.Doc.Pos()
		}
	}
	return d.Pos()
}

func declKey(d ast.Decl) string {
	switch x := d.(type) {
	case *ast.FuncDecl:
		if x.Recv == nil || len(x.Recv.List) == 0 {
			return "func " + x.Name.Name
		}
		typ := x.Recv.List[0].Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		switch g := typ.(type) {
		case *ast.IndexExpr:
			typ = g.X
		case *ast.IndexListExpr:
			typ = g.X
		}
		if id, ok := typ.(*ast.Ident); ok {
			return "func " + id.Name + "." + x.Name.Name
		}
		return "func ?." + x.Name.Name
	case *ast.GenDecl:
		var names []string
		for _, spec := range x.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				names = append(names, s.Name.Name)
			case *ast.ValueSpec:
				for _, id := range s.Names {
					names = append(names, id.Name)
				}
			}
		}
		return x.Tok.String() + " " + strings.Join(names, ",")
	}
	return "?"
}

// importName 给出导入的包名：names 表（go list 的结果）里有就用它，否则按惯例推断——
// 末段，去掉 go- 前缀与 .vN 后缀，末段是 vN 时取上一段。推断错了只会让这个导入
// 在两侧都记不上，比较结果依旧对称。
func importName(path string, names map[string]string) string {
	if name, ok := names[path]; ok {
		return name
	}
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = parts[len(parts)-2]
	}
	if i := strings.Index(last, ".v"); i > 0 {
		last = last[:i]
	}
	last = strings.TrimPrefix(last, "go-")
	return strings.NewReplacer("-", "_", ".", "_").Replace(last)
}

// usedImports 列出声明里「包名.X」用到的导入，形如 pgx=github.com/jackc/pgx/v5。
func usedImports(d ast.Decl, imports map[string]string) string {
	used := map[string]bool{}
	ast.Inspect(d, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
				used[id.Name] = true
			}
		}
		return true
	})
	var out []string
	for local := range used {
		if path, ok := imports[local]; ok {
			out = append(out, local+"="+path)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}
