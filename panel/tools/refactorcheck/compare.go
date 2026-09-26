// [INPUT]: 依赖 decls.go 的 collectDecls 与 tagUniverse，依赖 git 命令读取指定版本的源码
// [OUTPUT]: 对外提供 runCompare、compareFiles、compareResult
// [POS]: tools/refactorcheck 的 compare 子命令：逐包比对两个版本的顶层声明多重集合，相同才是纯挪动；顺带打印两侧各文件行数，供重构报告填「新旧行数」
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// compareResult 是一个包的比对结果。
type compareResult struct {
	baseDecls, headDecls int
	onlyBase, onlyHead   []string // 只在一侧出现的声明键
	changed              []string // 两侧都有但指纹不同的声明键
	floatingBase         []string // 只在 base 出现的游离注释
	floatingHead         []string
	baseLines, headLines map[string]int
}

func (r compareResult) ok() bool {
	return len(r.onlyBase) == 0 && len(r.onlyHead) == 0 && len(r.changed) == 0
}

// compareFiles 比对同一个包的两侧源文件。
func compareFiles(base, head []srcFile) (compareResult, error) {
	tags := map[string]bool{}
	if err := buildTagsOf(base, tags); err != nil {
		return compareResult{}, err
	}
	if err := buildTagsOf(head, tags); err != nil {
		return compareResult{}, err
	}
	var universe tagUniverse
	for tag := range tags {
		universe = append(universe, tag)
	}
	sort.Strings(universe)
	b, err := collectDecls(base, universe)
	if err != nil {
		return compareResult{}, err
	}
	h, err := collectDecls(head, universe)
	if err != nil {
		return compareResult{}, err
	}
	r := compareResult{baseLines: b.lines, headLines: h.lines}
	for key, values := range b.decls {
		r.baseDecls += len(values)
		other, ok := h.decls[key]
		switch {
		case !ok:
			r.onlyBase = append(r.onlyBase, key)
		case strings.Join(values, "\x00") != strings.Join(other, "\x00"):
			r.changed = append(r.changed, key)
		}
	}
	for key, values := range h.decls {
		r.headDecls += len(values)
		if _, ok := b.decls[key]; !ok {
			r.onlyHead = append(r.onlyHead, key)
		}
	}
	r.floatingBase, r.floatingHead = multisetDiff(b.floating, h.floating)
	sort.Strings(r.onlyBase)
	sort.Strings(r.onlyHead)
	sort.Strings(r.changed)
	return r, nil
}

// multisetDiff 返回两个已排序多重集合各自多出来的元素。
func multisetDiff(a, b []string) (onlyA, onlyB []string) {
	count := map[string]int{}
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
	}
	for s, n := range count {
		for ; n > 0; n-- {
			onlyA = append(onlyA, s)
		}
		for ; n < 0; n++ {
			onlyB = append(onlyB, s)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return onlyA, onlyB
}

//------------------------------------------------------------------------------
// 命令行
//------------------------------------------------------------------------------

func runCompare(args []string) (bool, error) {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	module := fs.String("C", ".", "module root to operate in")
	base := fs.String("base", "HEAD", "base git revision")
	head := fs.String("head", "", "head git revision (empty: working tree)")
	tests := fs.Bool("tests", false, "also compare _test.go files")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	dirs := fs.Args()
	if len(dirs) == 0 {
		var err error
		if dirs, err = changedDirs(*module, *base, *head); err != nil {
			return false, err
		}
	}
	headName := *head
	if headName == "" {
		headName = "worktree"
	}
	allOK := true
	for _, dir := range dirs {
		dir = filepath.ToSlash(filepath.Clean(dir))
		baseFiles, err := loadRevision(*module, *base, dir, *tests)
		if err != nil {
			return false, err
		}
		var headFiles []srcFile
		if *head == "" {
			headFiles, err = loadWorktree(*module, dir, *tests)
		} else {
			headFiles, err = loadRevision(*module, *head, dir, *tests)
		}
		if err != nil {
			return false, err
		}
		r, err := compareFiles(baseFiles, headFiles)
		if err != nil {
			return false, fmt.Errorf("%s: %v", dir, err)
		}
		printResult(dir, *base, headName, r)
		allOK = allOK && r.ok()
	}
	if allOK {
		fmt.Printf("\nPURE MOVE: %d package(s), every top-level declaration identical\n", len(dirs))
	} else {
		fmt.Printf("\nNOT A PURE MOVE: see DIFF lines above\n")
	}
	return allOK, nil
}

func printResult(dir, base, head string, r compareResult) {
	status := "OK  "
	if !r.ok() {
		status = "DIFF"
	}
	fmt.Printf("%s %s: %d decls in %d files (%s) -> %d decls in %d files (%s)\n",
		status, dir, r.baseDecls, len(r.baseLines), base, r.headDecls, len(r.headLines), head)
	fmt.Printf("     %s: %s\n", base, formatLines(r.baseLines))
	fmt.Printf("     %s: %s\n", head, formatLines(r.headLines))
	for _, key := range r.onlyBase {
		fmt.Printf("     DIFF only in %s: %s\n", base, key)
	}
	for _, key := range r.onlyHead {
		fmt.Printf("     DIFF only in %s: %s\n", head, key)
	}
	for _, key := range r.changed {
		fmt.Printf("     DIFF changed: %s\n", key)
	}
	for _, c := range r.floatingBase {
		fmt.Printf("     note: free-floating comment only in %s: %q\n", base, firstLine(c))
	}
	for _, c := range r.floatingHead {
		fmt.Printf("     note: free-floating comment only in %s: %q\n", head, firstLine(c))
	}
}

func formatLines(lines map[string]int) string {
	if len(lines) == 0 {
		return "(no files)"
	}
	names := make([]string, 0, len(lines))
	for name := range lines {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s(%d)", name, lines[name])
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

//------------------------------------------------------------------------------
// 读源码：git 版本与工作树
//------------------------------------------------------------------------------

func git(module string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", module}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, ee.Stderr)
		}
		return "", fmt.Errorf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func wantFile(name string, tests bool) bool {
	return strings.HasSuffix(name, ".go") && (tests || !strings.HasSuffix(name, "_test.go"))
}

// loadRevision 读 rev 版本里 dir 目录（相对 module）下的 .go 文件；目录不存在时返回空。
func loadRevision(module, rev, dir string, tests bool) ([]srcFile, error) {
	list, err := git(module, "ls-tree", "--name-only", rev, "--", dir+"/")
	if err != nil {
		return nil, err
	}
	var files []srcFile
	for _, path := range strings.Fields(list) {
		name := filepath.Base(path)
		if filepath.Dir(path) != dir || !wantFile(name, tests) {
			continue
		}
		src, err := git(module, "show", rev+":./"+path)
		if err != nil {
			return nil, err
		}
		files = append(files, srcFile{name: name, src: []byte(src)})
	}
	return files, nil
}

func loadWorktree(module, dir string, tests bool) ([]srcFile, error) {
	paths, err := filepath.Glob(filepath.Join(module, dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var files []srcFile
	for _, path := range paths {
		name := filepath.Base(path)
		if !wantFile(name, tests) {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, srcFile{name: name, src: src})
	}
	return files, nil
}

// changedDirs 列出两个版本之间有 .go 文件变化的目录（相对 module），testdata 除外。
func changedDirs(module, base, head string) ([]string, error) {
	var out string
	var err error
	if head == "" {
		out, err = git(module, "diff", "--name-only", "--relative", base, "--", "*.go")
		if err == nil {
			var untracked string
			untracked, err = git(module, "ls-files", "--others", "--exclude-standard", "--", "*.go")
			out += untracked
		}
	} else {
		out, err = git(module, "diff", "--name-only", "--relative", base, head, "--", "*.go")
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var dirs []string
	for _, path := range strings.Fields(out) {
		dir := filepath.ToSlash(filepath.Dir(path))
		if seen[dir] || dir == "testdata" || strings.HasPrefix(dir, "testdata/") || strings.Contains(dir, "/testdata") {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs, nil
}
