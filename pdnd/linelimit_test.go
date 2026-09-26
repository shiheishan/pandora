// [INPUT]: 依赖标准库 io/fs 从 module 根（本包目录）遍历全部 .go 文件，不依赖 git
// [OUTPUT]: 对外提供 TestGoFilesStayWithinLineLimit
// [POS]: pdnd 的行数守卫：第 5 阶段把 kernel 超 800 行的文件拆完之后，防止任何 .go 文件（含测试）再长回去；panel 在 tools/refactorcheck/linelimit_test.go 有同一道守卫
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

// 整个目录豁免：fork 来的第三方代码保持上游的文件划分，拆了就难再合上游。
// 目录不存在了（fork 被删或改名）本测试即红，要求同步这张表与根 CLAUDE.md。
var lineLimitExemptDirs = map[string]string{
	"internal/reality":     "fork 自 Go crypto/tls",
	"internal/realityquic": "fork 自 quic-go",
}

func TestGoFilesStayWithinLineLimit(t *testing.T) {
	for dir, why := range lineLimitExemptDirs {
		if info, err := os.Stat(filepath.FromSlash(dir)); err != nil || !info.IsDir() {
			t.Errorf("exempt directory %s (%s) no longer exists: remove it from lineLimitExemptDirs and the root CLAUDE.md", dir, why)
		}
	}
	files := 0
	var over []string
	lines := map[string]int{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(path)
		if d.IsDir() {
			if _, ok := lineLimitExemptDirs[rel]; ok {
				return filepath.SkipDir
			}
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		if n := bytes.Count(data, []byte("\n")); n > maxGoFileLines {
			over = append(over, rel)
			lines[rel] = n
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if files == 0 {
		t.Fatal("found no .go files in the module")
	}
	sort.Strings(over)
	for _, rel := range over {
		t.Errorf("%s has %d lines, over the %d-line limit: split it by topic the way kernel/vless.go was", rel, lines[rel], maxGoFileLines)
	}
}
