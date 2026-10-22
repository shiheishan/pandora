// 审计动作守卫的源码扫描器：按 go/ast 静态解析 panel/internal 下全部非测试 .go 文件里
// 写进 audit Event.Action 的代号；解析不了的写法记入 unresolved，让守卫测试变红（宁可红、不漏）。
package service

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const auditModulePath = "github.com/aegispanel/aegis"

type scanPkg struct {
	dir   string
	files []*scanFile
}

type scanFile struct {
	pkg     *scanPkg
	file    *ast.File
	imports map[string]string // 导入名 → 本仓库目录绝对路径；只登记 auditModulePath 下的导入
}

// site 是一个表达式出现的位置：所在文件与外层函数（包级声明时 fn 为 nil）。
type site struct {
	f  *scanFile
	fn *ast.FuncDecl
}

type actionScanner struct {
	fset     *token.FileSet
	panel    string
	auditDir string
	pkgs     map[string]*scanPkg
	visiting map[token.Pos]bool
}

func scanAuditActions(panelDir string) (actions map[string][]string, unresolved []string, err error) {
	panelDir, err = filepath.Abs(panelDir)
	if err != nil {
		return nil, nil, err
	}
	auditDir := filepath.Join(panelDir, "internal", "module", "audit", "service")

	s := &actionScanner{
		fset:     token.NewFileSet(),
		panel:    panelDir,
		auditDir: auditDir,
		pkgs:     map[string]*scanPkg{},
		visiting: map[token.Pos]bool{},
	}

	root := filepath.Join(panelDir, "internal")
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(s.fset, path, nil, 0)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		p := s.pkgs[dir]
		if p == nil {
			p = &scanPkg{dir: dir}
			s.pkgs[dir] = p
		}
		sf := &scanFile{
			pkg:     p,
			file:    file,
			imports: buildImports(panelDir, file),
		}
		p.files = append(p.files, sf)
		return nil
	}); err != nil {
		return nil, nil, err
	}

	actions = map[string][]string{}
	for _, p := range s.pkgs {
		for _, f := range p.files {
			s.walkTopLevel(f, func(n ast.Node, at site) {
				if lit, ok := n.(*ast.CompositeLit); ok && s.isAuditEventLit(f, lit) {
					s.recordEventLit(actions, &unresolved, lit, at)
				}
			})
		}
	}

	for k := range actions {
		slices.Sort(actions[k])
		actions[k] = slices.Compact(actions[k])
	}
	return actions, unresolved, nil
}

func buildImports(panelDir string, file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !strings.HasPrefix(path, auditModulePath+"/") {
			continue
		}
		rel := strings.TrimPrefix(path, auditModulePath+"/")
		dir := filepath.Join(panelDir, filepath.FromSlash(rel))
		name := filepath.Base(rel)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = dir
	}
	return out
}

func (s *actionScanner) relPos(pos token.Pos) string {
	p := s.fset.Position(pos)
	rel, err := filepath.Rel(s.panel, p.Filename)
	if err != nil {
		return p.String()
	}
	return rel + ":" + strconv.Itoa(p.Line) + ":" + strconv.Itoa(p.Column)
}

func (s *actionScanner) walkTopLevel(f *scanFile, visit func(ast.Node, site)) {
	for _, decl := range f.file.Decls {
		var at site
		at.f = f
		switch d := decl.(type) {
		case *ast.FuncDecl:
			at.fn = d
			if d.Body != nil {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if n != nil {
						visit(n, at)
					}
					return true
				})
			}
		case *ast.GenDecl:
			at.fn = nil
			ast.Inspect(d, func(n ast.Node) bool {
				if n != nil {
					visit(n, at)
				}
				return true
			})
		}
	}
}

func (s *actionScanner) walkSites(p *scanPkg, visit func(ast.Node, site)) {
	for _, f := range p.files {
		s.walkTopLevel(f, visit)
	}
}

func (s *actionScanner) isAuditEventLit(f *scanFile, lit *ast.CompositeLit) bool {
	if lit.Type == nil {
		return false
	}
	switch t := lit.Type.(type) {
	case *ast.SelectorExpr:
		if x, ok := t.X.(*ast.Ident); ok {
			if f.imports[x.Name] == s.auditDir && t.Sel.Name == "Event" {
				return true
			}
		}
	case *ast.Ident:
		if f.pkg.dir == s.auditDir && t.Name == "Event" {
			return true
		}
	}
	return false
}

func (s *actionScanner) recordEventLit(actions map[string][]string, unresolved *[]string, lit *ast.CompositeLit, at site) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Action" {
			continue
		}
		codes, err := s.resolve(kv.Value, at)
		pos := s.relPos(kv.Pos())
		if err != nil {
			*unresolved = append(*unresolved, fmt.Sprintf("%s: %v", pos, err))
			continue
		}
		for _, code := range codes {
			if code == "" {
				continue
			}
			actions[code] = append(actions[code], pos)
		}
	}
}

func (s *actionScanner) resolve(e ast.Expr, at site) ([]string, error) {
	if e == nil {
		return nil, fmt.Errorf("空表达式")
	}
	if s.visiting[e.Pos()] {
		return nil, nil
	}
	s.visiting[e.Pos()] = true
	defer func() { s.visiting[e.Pos()] = false }()

	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return nil, fmt.Errorf("%s: Action 不是字符串字面量", s.relPos(x.Pos()))
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return nil, fmt.Errorf("%s: 字符串字面量无效: %v", s.relPos(x.Pos()), err)
		}
		return []string{v}, nil
	case *ast.ParenExpr:
		return s.resolve(x.X, at)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, fmt.Errorf("%s: 不支持的写法 %T", s.relPos(x.Pos()), e)
		}
		left, err := s.resolve(x.X, at)
		if err != nil {
			return nil, err
		}
		right, err := s.resolve(x.Y, at)
		if err != nil {
			return nil, err
		}
		if len(left) == 0 || len(right) == 0 {
			return nil, nil
		}
		var out []string
		for _, a := range left {
			for _, b := range right {
				out = append(out, a+b)
			}
		}
		return out, nil
	case *ast.Ident:
		return s.resolveIdent(x, at)
	case *ast.SelectorExpr:
		return s.resolveSelector(x, at)
	default:
		return nil, fmt.Errorf("%s: 不支持的写法 %T", s.relPos(e.Pos()), e)
	}
}

func (s *actionScanner) resolveIdent(id *ast.Ident, at site) ([]string, error) {
	name := id.Name
	if at.fn != nil {
		idx := paramIndex(at.fn, name)
		if idx >= 0 {
			return s.resolveParam(at, idx)
		}
		vals, err := s.resolveLocalAssigns(at, name)
		if err != nil {
			return nil, err
		}
		if len(vals) > 0 {
			return vals, nil
		}
	}
	vals, err := s.resolvePackageValue(at.f.pkg, name, site{f: at.f, fn: nil})
	if err != nil {
		return nil, err
	}
	if len(vals) > 0 {
		return vals, nil
	}
	return nil, fmt.Errorf("%s: 解析不了标识符 %s", s.relPos(id.Pos()), name)
}

func paramIndex(fn *ast.FuncDecl, name string) int {
	if fn.Type.Params == nil {
		return -1
	}
	idx := 0
	for _, field := range fn.Type.Params.List {
		for _, n := range field.Names {
			if n.Name == name {
				return idx
			}
			idx++
		}
	}
	return -1
}

func (s *actionScanner) resolveLocalAssigns(at site, name string) ([]string, error) {
	if at.fn == nil || at.fn.Body == nil {
		return nil, nil
	}
	var out []string
	var err error
	ast.Inspect(at.fn.Body, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		switch st := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range st.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || id.Name != name {
					continue
				}
				if len(st.Lhs) != len(st.Rhs) {
					// v, err := f() 这类多返回值赋给了动作变量：静态取不到值
					err = fmt.Errorf("%s: %s 来自多返回值赋值，解析不了", s.relPos(st.Pos()), name)
					return false
				}
				vals, e := s.resolve(st.Rhs[i], at)
				if e != nil {
					err = e
					return false
				}
				out = append(out, vals...)
			}
		case *ast.DeclStmt:
			gd, ok := st.Decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name != name {
						continue
					}
					if len(vs.Values) != len(vs.Names) {
						err = fmt.Errorf("%s: var 声明的 %s 没有对应的值，解析不了", s.relPos(vs.Pos()), name)
						return false
					}
					vals, e := s.resolve(vs.Values[i], at)
					if e != nil {
						err = e
						return false
					}
					out = append(out, vals...)
				}
			}
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *actionScanner) resolvePackageValue(p *scanPkg, name string, at site) ([]string, error) {
	var out []string
	for _, f := range p.files {
		for _, decl := range f.file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if n.Name != name {
						continue
					}
					if len(vs.Values) != len(vs.Names) {
						return nil, fmt.Errorf("%s: 包级名与值数量不等", s.relPos(n.Pos()))
					}
					atDecl := site{f: f, fn: nil}
					vals, err := s.resolve(vs.Values[i], atDecl)
					if err != nil {
						return nil, err
					}
					out = append(out, vals...)
				}
			}
		}
	}
	return out, nil
}

func (s *actionScanner) resolveSelector(sel *ast.SelectorExpr, at site) ([]string, error) {
	if x, ok := sel.X.(*ast.Ident); ok {
		if dir, ok := at.f.imports[x.Name]; ok {
			p := s.pkgs[dir]
			if p == nil {
				return nil, fmt.Errorf("%s: 导入包 %s 未解析", s.relPos(sel.Pos()), x.Name)
			}
			vals, err := s.resolvePackageValue(p, sel.Sel.Name, site{f: at.f, fn: nil})
			if err != nil {
				return nil, err
			}
			if len(vals) == 0 {
				return nil, fmt.Errorf("%s: 包 %s 里没有常量或变量 %s", s.relPos(sel.Pos()), x.Name, sel.Sel.Name)
			}
			return vals, nil
		}
	}
	return s.resolveField(at.f.pkg, sel.Sel.Name)
}

func (s *actionScanner) resolveField(p *scanPkg, name string) ([]string, error) {
	var out []string
	var found bool
	var firstErr error
	add := func(e ast.Expr, at site) {
		found = true
		vals, err := s.resolve(e, at)
		if err != nil {
			firstErr = err
			return
		}
		out = append(out, vals...)
	}
	s.walkSites(p, func(n ast.Node, at site) {
		if firstErr != nil {
			return
		}
		if lit, ok := n.(*ast.CompositeLit); ok {
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != name {
					continue
				}
				add(kv.Value, at)
			}
		}
		if st, ok := n.(*ast.AssignStmt); ok {
			for i, lhs := range st.Lhs {
				se, ok := lhs.(*ast.SelectorExpr)
				if !ok || se.Sel.Name != name {
					continue
				}
				if len(st.Lhs) != len(st.Rhs) {
					found = true
					firstErr = fmt.Errorf("%s: 字段 %s 来自多返回值赋值，解析不了", s.relPos(st.Pos()), name)
					return
				}
				add(st.Rhs[i], at)
			}
		}
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if !found {
		return nil, fmt.Errorf("字段 %s 在包 %s 里没有赋值处", name, strings.TrimPrefix(p.dir, s.panel+"/"))
	}
	return out, nil
}

func (s *actionScanner) resolveParam(at site, idx int) ([]string, error) {
	if at.fn == nil {
		return nil, fmt.Errorf("%s: 没有外层函数", s.relPos(at.f.file.Pos()))
	}
	name := at.fn.Name.Name
	searchAll := len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
	var pkgs []*scanPkg
	if searchAll {
		for _, p := range s.pkgs {
			pkgs = append(pkgs, p)
		}
	} else {
		pkgs = []*scanPkg{at.f.pkg}
	}

	var out []string
	var found bool
	var resolveErr error
	for _, p := range pkgs {
		s.walkSites(p, func(n ast.Node, callAt site) {
			if resolveErr != nil {
				return
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return
			}
			if !s.callMatches(call, name, at.fn) {
				return
			}
			if len(call.Args) <= idx {
				return
			}
			found = true
			vals, err := s.resolve(call.Args[idx], callAt)
			if err != nil {
				resolveErr = err
				return
			}
			out = append(out, vals...)
		})
	}
	if resolveErr != nil {
		return nil, resolveErr
	}
	if !found {
		return nil, fmt.Errorf("%s: 包装函数 %s 没有调用方", s.relPos(at.fn.Pos()), name)
	}
	return out, nil
}

func (s *actionScanner) callMatches(call *ast.CallExpr, name string, wrapped *ast.FuncDecl) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if wrapped.Recv != nil {
			return false
		}
		return fun.Name == name
	case *ast.SelectorExpr:
		return fun.Sel.Name == name
	default:
		return false
	}
}
