// Package migrationlint 是 panel/migrations 的 DDL 守卫：每个迁移必须有 Down（或
// irreversible 标记），开头设锁等待与语句超时，大表上不许阻塞建索引、不许整表重写、
// 改名删列走「扩展 → 迁移数据 → 收缩」、回填要声明分批与可重跑。
//
// 历史违例登记在 ratchet.txt，只许删不许加；Up 段冻结在 upsegments.txt。规则与做法见
// .claude/rules/panel-migrations.md 和 panel/deploy/MIGRATION-RUNBOOK.md。
//
// 只依赖标准库；不进发布包，由 panel 的 go test ./... 执行。
package migrationlint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Migration 是一个迁移文件拆开后的样子。
type Migration struct {
	Version int
	Name    string // 文件名，如 00016_node_kernel.sql
	// Header 是 `-- +goose Up` 之前的行（文件头），标记都写在这里。
	Header []string
	// Up、Down 是两个段的原文，不含标记行本身。
	Up, Down string
	// UpRaw 是从 Up 标记行起、到 Down 标记行之前的原文，用来冻结 Up 段。
	UpRaw         string
	HasUp         bool
	HasDown       bool
	NoTransaction bool
}

var (
	fileNameRe     = regexp.MustCompile(`^([0-9]{5})_[A-Za-z0-9._-]+\.sql$`)
	upMarkerRe     = regexp.MustCompile(`^-- \+goose Up\s*$`)
	downMarkerRe   = regexp.MustCompile(`^-- \+goose Down\s*$`)
	noTxMarkerRe   = regexp.MustCompile(`^-- \+goose NO TRANSACTION\s*$`)
	headerMarkerRe = regexp.MustCompile(`^--\s*([a-z][a-z-]*):\s*(.*)$`)
)

// ParseFile 读一个迁移文件。
func ParseFile(path string) (Migration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Migration{}, err
	}
	return Parse(filepath.Base(path), string(raw))
}

// Parse 按 goose 的标记行把文件拆成文件头、Up、Down 三段。
func Parse(name, text string) (Migration, error) {
	m := Migration{Name: name}
	match := fileNameRe.FindStringSubmatch(name)
	if match == nil {
		return m, fmt.Errorf("%s: 文件名不是 00000_name.sql 形式", name)
	}
	m.Version, _ = strconv.Atoi(match[1])

	lines := strings.SplitAfter(text, "\n")
	section := 0 // 0 文件头，1 Up，2 Down
	var up, upRaw, down strings.Builder
	for _, line := range lines {
		bare := strings.TrimRight(line, "\r\n")
		if noTxMarkerRe.MatchString(bare) {
			m.NoTransaction = true
		}
		switch {
		case upMarkerRe.MatchString(bare):
			if m.HasUp {
				return m, fmt.Errorf("%s: 出现两个 Up 标记", name)
			}
			m.HasUp, section = true, 1
			upRaw.WriteString(line)
			continue
		case downMarkerRe.MatchString(bare):
			if m.HasDown {
				return m, fmt.Errorf("%s: 出现两个 Down 标记", name)
			}
			if !m.HasUp {
				return m, fmt.Errorf("%s: Down 标记出现在 Up 之前", name)
			}
			m.HasDown, section = true, 2
			continue
		}
		switch section {
		case 0:
			m.Header = append(m.Header, bare)
		case 1:
			up.WriteString(line)
			upRaw.WriteString(line)
		case 2:
			down.WriteString(line)
		}
	}
	if !m.HasUp {
		return m, fmt.Errorf("%s: 没有 `-- +goose Up` 标记", name)
	}
	m.Up, m.UpRaw, m.Down = up.String(), upRaw.String(), down.String()
	return m, nil
}

// HeaderValue 取文件头里 `-- key: value` 的所有取值。
func (m Migration) HeaderValue(key string) []string {
	var out []string
	for _, line := range m.Header {
		if g := headerMarkerRe.FindStringSubmatch(line); g != nil && g[1] == key {
			out = append(out, strings.TrimSpace(g[2]))
		}
	}
	return out
}

// Irreversible 表示文件头写了 `-- irreversible: <原因>`。
func (m Migration) Irreversible() bool {
	for _, v := range m.HeaderValue("irreversible") {
		if v != "" {
			return true
		}
	}
	return false
}

// UpSegment 是用来冻结的 Up 段：从 Up 标记行到 Down 标记行之前，去掉末尾空行。
// 末尾空行不算内容：给历史迁移追加 Down 时，Down 标记前会多一个空行。
func (m Migration) UpSegment() string {
	return strings.TrimRight(m.UpRaw, " \t\r\n") + "\n"
}

// LoadDir 读目录下全部 *.sql 迁移，按版本号排序。
func LoadDir(dir string) ([]Migration, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, p := range paths {
		m, err := ParseFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
