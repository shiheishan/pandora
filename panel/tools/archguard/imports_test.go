package archguard

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/aegispanel/aegis/"

// importRules 是面板的依赖方向（.claude/rules/panel-architecture.md）：左边的包不许
// import 右边的任何包。只看非测试源码：测试为了造夹具跨层引用不影响产品的分层。
var importRules = []struct{ from, to string }{
	{"internal/platform/", "internal/domain/"},
	{"internal/platform/", "internal/api/"},
	{"internal/platform/", "internal/middleware"},
	{"internal/domain/", "internal/api/"},
	{"internal/middleware", "internal/domain/"},
	{"internal/middleware", "internal/api/"},
}

// importExemptions 是现有的跨层引用（「引用方 -> 被引用包」，路径相对 panel/），逐条写明
// 理由。棘轮：只许删不许加；那条引用消失后豁免同样变红，逼着一起删。
var importExemptions = map[string]string{
	"internal/platform/idempotencybind -> internal/middleware": "幂等认领与业务写入同一事务，绑定层要拿 middleware 的 IdempotencyClaim（待认领 SQL 迁出 middleware 后一并移走）",
}

func under(pkg, prefix string) bool {
	if strings.HasSuffix(prefix, "/") {
		return strings.HasPrefix(pkg, prefix)
	}
	return pkg == prefix || strings.HasPrefix(pkg, prefix+"/")
}

func TestImportDirection(t *testing.T) {
	panel := filepath.Join("..", "..")
	found := map[string][]string{} // 「引用方 -> 被引用包」→ 出处
	files := 0
	err := filepath.WalkDir(filepath.Join(panel, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		relFile, _ := filepath.Rel(panel, path)
		pkg := filepath.ToSlash(filepath.Dir(relFile))
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.HasPrefix(p, modulePath) {
				continue
			}
			target := strings.TrimPrefix(p, modulePath)
			for _, r := range importRules {
				if under(pkg, r.from) && under(target, r.to) {
					key := pkg + " -> " + target
					found[key] = append(found[key], filepath.ToSlash(relFile))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 300 {
		t.Fatalf("scanned only %d files under internal/; the walk is broken", files)
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := importExemptions[k]; !ok {
			t.Errorf("import against the layering (api → domain → platform, middleware before api): %s\n  in %s",
				k, strings.Join(found[k], ", "))
		}
	}
	for k := range importExemptions {
		if len(found[k]) == 0 {
			t.Errorf("exemption %q no longer occurs; delete it from importExemptions", k)
		}
	}
}

func TestImportRuleMatching(t *testing.T) {
	for _, c := range []struct {
		pkg, prefix string
		want        bool
	}{
		{"internal/platform/db", "internal/platform/", true},
		{"internal/middleware", "internal/middleware", true},
		{"internal/middleware/x", "internal/middleware", true},
		{"internal/middlewarex", "internal/middleware", false},
		{"internal/domain/billing", "internal/api/", false},
	} {
		if got := under(c.pkg, c.prefix); got != c.want {
			t.Errorf("under(%q, %q) = %v", c.pkg, c.prefix, got)
		}
	}
}
