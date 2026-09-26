// [INPUT]: 依赖标准库 io/fs 遍历 module 根目录（向上找 go.mod），不依赖 git
// [OUTPUT]: 对外提供 TestGoFilesStayWithinLineLimit
// [POS]: tools/refactorcheck 的行数守卫：第 5 阶段把超 800 行的文件拆完之后，防止任何 .go 文件（含测试）再长回去；pdnd 在 module 根的 linelimit_test.go 有同一道守卫
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 与根 CLAUDE.md「单文件 ≤800 行」同一个数，按 wc -l 的口径数换行符。
const maxGoFileLines = 800

// 逐个登记的豁免。每一项都是「纯挪动拆不开」的文件：整个文件只剩一个超长的
// 测试函数，子测试共享同一组连接与上下文，要再拆就得改函数体，超出第 5 阶段
// 「只挪代码」的授权，等协调会话另派。文件拆到 800 行以内或被删掉时，本测试
// 会要求把它从这里移走——豁免过期即红，不留死条目。
var lineLimitExemptFiles = map[string]string{
	"internal/middleware/idempotency_pg18_test.go":       "TestIdempotencyMiddlewarePG18 单个函数 983 行",
	"internal/domain/billing/order_release_pg18_test.go": "TestOrderReleasePG18 单个函数 832 行",
}

func TestGoFilesStayWithinLineLimit(t *testing.T) {
	root := moduleRoot(t)
	seen := map[string]int{}
	var over []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		lines := bytes.Count(data, []byte("\n"))
		seen[rel] = lines
		if lines > maxGoFileLines {
			if _, ok := lineLimitExemptFiles[rel]; !ok {
				over = append(over, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(seen) == 0 {
		t.Fatalf("found no .go files under %s", root)
	}
	sort.Strings(over)
	for _, rel := range over {
		t.Errorf("%s has %d lines, over the %d-line limit: split it by topic (see panel/tools/refactorcheck/CLAUDE.md)", rel, seen[rel], maxGoFileLines)
	}
	for rel, why := range lineLimitExemptFiles {
		lines, ok := seen[rel]
		switch {
		case !ok:
			t.Errorf("exempt file %s (%s) no longer exists: remove it from lineLimitExemptFiles", rel, why)
		case lines <= maxGoFileLines:
			t.Errorf("exempt file %s is down to %d lines: remove it from lineLimitExemptFiles", rel, lines)
		}
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
