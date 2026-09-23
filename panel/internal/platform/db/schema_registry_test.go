// [INPUT]: 依赖 migrations/*.sql goose Up 段里按序号重放的 CREATE / DROP TABLE、migrations/RESERVED-TABLES.md 登记簿，以及 internal/、cmd/、web/ 下的非测试 Go 源码
// [OUTPUT]: 对外提供 TestSchemaTablesAreReferencedOrRegistered 契约测试
// [POS]: platform/db 的 schema 同构守卫：迁移最终留下的表而 Go 从不引用的必须登记在 RESERVED-TABLES.md，登记了却被引用或已被后续迁移删除的必须删掉登记
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package db

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

var (
	tableDDLPattern   = regexp.MustCompile(`(?im)^\s*(CREATE|DROP) TABLE (?:IF (?:NOT )?EXISTS )?([a-z_][a-z0-9_]*)`)
	gooseDownPattern  = regexp.MustCompile(`(?m)^-- \+goose Down`)
	identifierPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

func TestSchemaTablesAreReferencedOrRegistered(t *testing.T) {
	root := schemaRegistryRoot(t)
	tables := migrationTables(t, root)
	source := nonTestGoSource(t, root)
	registered := registeredTables(t, root)

	var unregistered, stale []string
	for _, table := range tables {
		referenced := regexp.MustCompile(`\b` + regexp.QuoteMeta(table) + `\b`).MatchString(source)
		switch {
		case !referenced && !registered[table]:
			unregistered = append(unregistered, table)
		case referenced && registered[table]:
			stale = append(stale, table)
		}
	}
	if len(unregistered) > 0 {
		t.Errorf("迁移建了表但 Go 代码从不引用，也没有登记在 migrations/RESERVED-TABLES.md：%s", strings.Join(unregistered, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("已登记为无代码引用，但 Go 代码现在引用了它，请从 migrations/RESERVED-TABLES.md 删除：%s", strings.Join(stale, ", "))
	}

	known := map[string]bool{}
	for _, table := range tables {
		known[table] = true
	}
	var unknown []string
	for name := range registered {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		t.Errorf("RESERVED-TABLES.md 登记了迁移没有创建或已被后续迁移删除的表：%s", strings.Join(unknown, ", "))
	}
}

func schemaRegistryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
}

// migrationTables 返回按序号跑完全部 Up 之后仍然存在的表。
// 只看 migrations/ 顶层的 goose 文件，不含 frozen-client-auth/；只读 Up 段：
// Down 里为回滚重建的表不是现行 schema。Glob 结果按文件名排序，即按迁移序号重放。
func migrationTables(t *testing.T, root string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	seen := map[string]bool{}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		up := string(body)
		if at := gooseDownPattern.FindStringIndex(up); at != nil {
			up = up[:at[0]]
		}
		for _, match := range tableDDLPattern.FindAllStringSubmatch(up, -1) {
			if strings.EqualFold(match[1], "CREATE") {
				seen[match[2]] = true
			} else {
				delete(seen, match[2])
			}
		}
	}
	tables := make([]string, 0, len(seen))
	for name := range seen {
		tables = append(tables, name)
	}
	sort.Strings(tables)
	return tables
}

func nonTestGoSource(t *testing.T, root string) string {
	t.Helper()
	var builder strings.Builder
	for _, dir := range []string{"internal", "cmd", "web"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			builder.Write(body)
			builder.WriteByte('\n')
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return builder.String()
}

// registeredTables 读登记簿里 Markdown 表格的第一列；表头与分隔行不是合法标识符，自然被跳过。
func registeredTables(t *testing.T, root string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "migrations", "RESERVED-TABLES.md"))
	if err != nil {
		t.Fatalf("read RESERVED-TABLES.md: %v", err)
	}
	registered := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		name := strings.TrimSpace(cells[1])
		if identifierPattern.MatchString(name) {
			registered[name] = true
		}
	}
	if len(registered) == 0 {
		t.Fatal("RESERVED-TABLES.md registers no tables")
	}
	return registered
}
