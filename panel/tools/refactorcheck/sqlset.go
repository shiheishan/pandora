package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//------------------------------------------------------------------------------
// 识别与规范化
//------------------------------------------------------------------------------

// sqlMarker 判断一个字面量是不是 SQL 或 SQL 片段：仓库约定 SQL 关键字一律大写，
// 动态拼接的片段（" AND status = $2"）靠大写关键字或 $n 占位符认出来。
// 英文提示语都是小写，误认的极少；误认了也只是两侧同时多出一条，挪动时不影响结论。
var sqlMarker = regexp.MustCompile(`\b(SELECT|INSERT|UPDATE|DELETE|FROM|WHERE|JOIN|RETURNING|VALUES|ORDER BY|GROUP BY|LIMIT|OFFSET|ON CONFLICT|AND|OR|SET|COALESCE)\b|\$[0-9]+`)

func looksLikeSQL(s string) bool { return sqlMarker.MatchString(s) }

// normalizeSQL 把连续空白压成一个空格并去掉首尾空白：挪动时缩进会变，SQL 本身不能变。
func normalizeSQL(s string) string { return strings.Join(strings.Fields(s), " ") }

// sqlSite 是一条 SQL 字面量及其所在文件（只用于报告差异）。
type sqlSite struct{ sql, file string }

// collectSQL 收集一个文件里全部像 SQL 的字符串字面量（规范化后）。
// 结构体标签不是 SQL，跳过，免得「map 改 DTO」之类的改动混进来。
func collectSQL(path string, src []byte) ([]sqlSite, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	tags := map[*ast.BasicLit]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if field, ok := n.(*ast.Field); ok && field.Tag != nil {
			tags[field.Tag] = true
		}
		return true
	})
	var out []sqlSite
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || tags[lit] {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err == nil && looksLikeSQL(s) {
			out = append(out, sqlSite{sql: normalizeSQL(s), file: path})
		}
		return true
	})
	return out, nil
}

//------------------------------------------------------------------------------
// 读整棵目录树：git 版本与工作树
//------------------------------------------------------------------------------

func skipTreePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "testdata" || part == "node_modules" || (strings.HasPrefix(part, ".") && part != "." && part != "..") {
			return true
		}
	}
	return false
}

func sqlOfRevision(module, rev, root string, tests bool) ([]sqlSite, error) {
	list, err := git(module, "ls-tree", "-r", "--name-only", rev, "--", root+"/")
	if err != nil {
		return nil, err
	}
	var all []sqlSite
	for _, path := range strings.Fields(list) {
		if !wantFile(filepath.Base(path), tests) || skipTreePath(path) {
			continue
		}
		src, err := git(module, "show", rev+":./"+path)
		if err != nil {
			return nil, err
		}
		sites, err := collectSQL(path, []byte(src))
		if err != nil {
			return nil, err
		}
		all = append(all, sites...)
	}
	return all, nil
}

func sqlOfWorktree(module, root string, tests bool) ([]sqlSite, error) {
	var all []sqlSite
	err := filepath.WalkDir(filepath.Join(module, root), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(module, path)
		if d.IsDir() {
			if rel != root && skipTreePath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !wantFile(d.Name(), tests) {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sites, err := collectSQL(filepath.ToSlash(rel), src)
		all = append(all, sites...)
		return err
	})
	return all, err
}

//------------------------------------------------------------------------------
// 命令行
//------------------------------------------------------------------------------

func runSQLSet(args []string) (bool, error) {
	fs := flag.NewFlagSet("sqlset", flag.ContinueOnError)
	module := fs.String("C", ".", "module root to operate in")
	base := fs.String("base", "HEAD", "base git revision")
	head := fs.String("head", "", "head git revision (empty: working tree)")
	root := fs.String("root", "internal", "directory tree (relative to module) to collect SQL from")
	tests := fs.Bool("tests", false, "also collect SQL from _test.go files")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	dir := filepath.ToSlash(filepath.Clean(*root))
	baseSites, err := sqlOfRevision(*module, *base, dir, *tests)
	if err != nil {
		return false, err
	}
	var headSites []sqlSite
	headName := *head
	if headName == "" {
		headName = "worktree"
		headSites, err = sqlOfWorktree(*module, dir, *tests)
	} else {
		headSites, err = sqlOfRevision(*module, *head, dir, *tests)
	}
	if err != nil {
		return false, err
	}
	onlyBase, onlyHead := multisetDiff(sortedSQL(baseSites), sortedSQL(headSites))
	fmt.Printf("%s: %d SQL literal(s) in %s, %d in %s\n", dir, len(baseSites), *base, len(headSites), headName)
	report(*base, onlyBase, baseSites)
	report(headName, onlyHead, headSites)
	if len(onlyBase) == 0 && len(onlyHead) == 0 {
		fmt.Printf("\nSQL UNCHANGED: the multiset of SQL literals under %s is identical\n", dir)
		return true, nil
	}
	fmt.Printf("\nSQL CHANGED: explain every DIFF line above in the report\n")
	return false, nil
}

func sortedSQL(sites []sqlSite) []string {
	out := make([]string, len(sites))
	for i, s := range sites {
		out[i] = s.sql
	}
	sort.Strings(out)
	return out
}

// report 列出只在一侧出现的 SQL，并给出它在该侧出现过的文件，方便逐条说明理由。
func report(side string, only []string, sites []sqlSite) {
	for _, sql := range only {
		files := map[string]bool{}
		for _, s := range sites {
			if s.sql == sql {
				files[s.file] = true
			}
		}
		names := make([]string, 0, len(files))
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		fmt.Printf("DIFF only in %s [%s]: %s\n", side, strings.Join(names, " "), sql)
	}
}
