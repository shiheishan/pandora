// [INPUT]: 依赖 decls.go 的 importName 与 fileNameTags，依赖 git ls-files 列出要复制的文件、go list 给出导入的真实包名
// [OUTPUT]: 对外提供 runShatter、shatterFile
// [POS]: tools/refactorcheck 的 shatter 子命令：在仓库副本里把每个顶层声明拆进随机命名的独立文件，声明先后随之打乱；副本上测试全绿即证明测试不依赖文件名与声明顺序
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func runShatter(args []string) error {
	fs := flag.NewFlagSet("shatter", flag.ContinueOnError)
	module := fs.String("C", ".", "module root to operate in")
	out := fs.String("out", "", "directory to create the shattered copy in (must not exist)")
	keep := fs.String("keep", `^router(_[a-z0-9_]+)?\.go$`,
		"regexp of file base names left intact (default keeps api/admin's route table files, read by design)")
	skip := fs.String("skip", "", "regexp of module-relative package dirs left intact")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("shatter needs -out")
	}
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists", *out)
	}
	keepRE, err := regexp.Compile(*keep)
	if err != nil {
		return err
	}
	var skipRE *regexp.Regexp
	if *skip != "" {
		if skipRE, err = regexp.Compile(*skip); err != nil {
			return err
		}
	}

	// 复制：仓库里被跟踪与未跟踪（未被忽略）的全部文件，保证 ../pdnd、frontend 源码这类跨目录读取的测试也在
	top, err := git(*module, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top = strings.TrimSpace(top)
	absModule, err := filepath.Abs(*module)
	if err != nil {
		return err
	}
	absModule, _ = filepath.EvalSymlinks(absModule)
	realTop, _ := filepath.EvalSymlinks(top)
	rel, err := filepath.Rel(realTop, absModule)
	if err != nil {
		return err
	}
	list, err := git(top, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	copied := 0
	for _, path := range strings.Split(list, "\x00") {
		if path == "" {
			continue
		}
		if err := copyFile(filepath.Join(top, path), filepath.Join(*out, path)); err != nil {
			return err
		}
		copied++
	}
	copyModule := filepath.Join(*out, rel)

	dirs := fs.Args()
	if len(dirs) == 0 {
		listed, err := goList(copyModule, nil, "-f", "{{.Dir}}", "./...")
		if err != nil {
			return err
		}
		for _, d := range strings.Fields(listed) {
			r, _ := filepath.Rel(copyModule, d)
			dirs = append(dirs, r)
		}
	}
	names := map[string]string{}
	for _, goos := range []string{"", "linux"} {
		var env []string
		if goos != "" {
			env = []string{"GOOS=" + goos}
		}
		listed, err := goList(copyModule, env, "-deps", "-f", "{{.ImportPath}} {{.Name}}", "./...")
		if err != nil {
			return err
		}
		for _, line := range strings.Split(listed, "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				names[f[0]] = f[1]
			}
		}
	}

	var packages, before, after int
	for _, dir := range dirs {
		dir = filepath.ToSlash(filepath.Clean(dir))
		if skipRE != nil && skipRE.MatchString(dir) {
			fmt.Printf("skip %s\n", dir)
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(copyModule, dir, "*.go"))
		n := 0
		for _, path := range paths {
			base := filepath.Base(path)
			if strings.HasSuffix(base, "_test.go") || keepRE.MatchString(base) {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			chunks, err := shatterFile(srcFile{name: base, src: src}, names)
			if err != nil {
				return fmt.Errorf("%s: %v", path, err)
			}
			if len(chunks) == 0 { // 只有导入或包注释的文件原样保留，空白导入的副作用不能丢
				continue
			}
			for _, chunk := range chunks {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), chunk.name), chunk.src, 0o644); err != nil {
					return err
				}
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			before++
			after += len(chunks)
			n++
		}
		if n > 0 {
			packages++
		}
	}
	fmt.Printf("copied %d files to %s\nshattered %d files into %d single-declaration files in %d packages\n",
		copied, *out, before, after, packages)
	fmt.Printf("next: cd %s && go vet ./... && GOOS=linux go vet ./... && go test -p 1 -count=1 ./...\n", copyModule)
	return nil
}

func goList(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("go list: %v: %s", err, ee.Stderr)
		}
		return "", err
	}
	return string(out), nil
}

func copyFile(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		if os.IsNotExist(err) { // 工作树里删了但还在索引里
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		return os.Symlink(target, to)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// shatterFile 把一个源文件拆成每个顶层声明一个文件。
//
// 每块取「上一个声明结束处到本声明结束处」的原文，前面的文档注释与分节注释随之同行；
// 最后一块带上文件尾部。//go:build 行原样复制，文件名保留 _GOOS_GOARCH 后缀，
// 构建约束因此不变。导入按块内「包名.X」的用法挑选，空白导入每块都带。
func shatterFile(f srcFile, names map[string]string) ([]srcFile, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.name, f.src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }

	var header strings.Builder
	for _, line := range strings.Split(string(f.src[:off(file.Package)]), "\n") {
		if strings.HasPrefix(line, "//go:build ") {
			header.WriteString(line + "\n\n")
		}
	}
	header.WriteString("package " + file.Name.Name + "\n\n")

	type imp struct {
		local, spec string
		always      bool
	}
	var imports []imp
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		local, always := importName(path, names), false
		if spec.Name != nil {
			local = spec.Name.Name
			always = local == "_"
			if local == "." {
				return nil, fmt.Errorf("dot import of %s is not supported", path)
			}
		}
		imports = append(imports, imp{local, string(f.src[off(spec.Pos()):off(spec.End())]), always})
	}

	var decls []ast.Decl
	start := off(file.Name.End())
	for _, d := range file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			start = off(d.End())
			continue
		}
		decls = append(decls, d)
	}
	if len(decls) == 0 {
		return nil, nil
	}
	suffix := ""
	if tags := fileNameTags(f.name); len(tags) > 0 {
		suffix = "_" + strings.Join(tags, "_")
	}
	var out []srcFile
	for i, d := range decls {
		end := off(d.End())
		if i == len(decls)-1 {
			end = len(f.src)
		}
		used := map[string]bool{}
		ast.Inspect(d, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
					used[id.Name] = true
				}
			}
			return true
		})
		var b bytes.Buffer
		b.WriteString(header.String())
		b.WriteString("import (\n")
		for _, im := range imports {
			if im.always || used[im.local] {
				b.WriteString("\t" + im.spec + "\n")
			}
		}
		b.WriteString(")\n")
		b.Write(f.src[start:end])
		b.WriteString("\n")
		start = end
		out = append(out, srcFile{name: randomName(suffix), src: b.Bytes()})
	}
	return out, nil
}

func randomName(suffix string) string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return "zz" + hex.EncodeToString(buf) + suffix + ".go"
}
