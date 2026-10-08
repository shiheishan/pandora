package migrationlint

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ratchetCeiling 是登记历史违例时主线上最大的迁移号。ratchet.txt 只许登记不超过它的
// 迁移：之后的新迁移必须一次合规，不能靠登记豁免。
const ratchetCeiling = 133

func panelRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func loadReal(t *testing.T) []Migration {
	t.Helper()
	ms, err := LoadDir(filepath.Join(panelRoot(t), "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 100 {
		t.Fatalf("只读到 %d 个迁移，目录不对", len(ms))
	}
	return ms
}

// readList 读「每行一条、# 开头是注释」的登记文件。
func readList(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(panelRoot(t), "tools", "migrationlint", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// 新违例即红；登记了却已不再违例的条目也红（只许删不许加，删要删干净）。
func TestMigrationLintRatchet(t *testing.T) {
	ms := loadReal(t)
	got := map[string][]string{}
	for _, v := range LintAll(ms) {
		got[v.Key()] = append(got[v.Key()], v.Detail)
	}
	registered := map[string]bool{}
	for _, line := range readList(t, "ratchet.txt") {
		if registered[line] {
			t.Errorf("ratchet.txt 重复登记：%s", line)
		}
		registered[line] = true
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("ratchet.txt 行格式应为「文件名 规则」：%q", line)
			continue
		}
		version, err := strconv.Atoi(strings.SplitN(fields[0], "_", 2)[0])
		if err != nil || version > ratchetCeiling {
			t.Errorf("ratchet.txt 只登记 %05d 及以前的历史迁移，新迁移必须一次合规：%s", ratchetCeiling, line)
		}
	}
	var fresh []string
	for key, details := range got {
		if !registered[key] {
			for _, d := range details {
				fresh = append(fresh, key+": "+d)
			}
		}
	}
	sort.Strings(fresh)
	for _, f := range fresh {
		t.Errorf("新违例（改迁移，不要往 ratchet.txt 里加）：%s", f)
	}
	var stale []string
	for key := range registered {
		if _, ok := got[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("ratchet.txt 的条目已不再违例，请删掉这一行：%s", s)
	}
}

// 每个迁移都必须有 Down 或 irreversible 标记，这一条没有历史豁免。
func TestEveryMigrationHasDownOrIrreversible(t *testing.T) {
	for _, m := range loadReal(t) {
		if !m.HasDown {
			t.Errorf("%s 没有 `-- +goose Down` 段", m.Name)
		}
		if m.Irreversible() {
			for _, v := range Lint(m, nil) {
				if v.Rule == RuleIrreversibleIncomplete {
					t.Errorf("%s", v)
				}
			}
		}
	}
}

func upSegmentDigest(m Migration) string {
	sum := sha256.Sum256([]byte(m.UpSegment()))
	return hex.EncodeToString(sum[:])
}

// 已发布迁移的 Up 段冻结：线上库已经跑过它们，改了文件只会让新装与存量分叉。
// 补 Down、加文件头标记不动 Up 段，这里照样通过。
func TestPublishedUpSegmentsAreFrozen(t *testing.T) {
	byName := map[string]Migration{}
	for _, m := range loadReal(t) {
		byName[m.Name] = m
	}
	lines := readList(t, "upsegments.txt")
	if len(lines) < 100 {
		t.Fatalf("upsegments.txt 只有 %d 条", len(lines))
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("upsegments.txt 行格式应为「sha256 文件名」：%q", line)
			continue
		}
		m, ok := byName[fields[1]]
		if !ok {
			t.Errorf("已发布的迁移不见了（不许删、不许改名）：%s", fields[1])
			continue
		}
		if got := upSegmentDigest(m); got != fields[0] {
			t.Errorf("%s 的 Up 段变了（%s ≠ 登记的 %s）：已发布的迁移只许追加 Down 段和文件头标记", fields[1], got, fields[0])
		}
	}
}
