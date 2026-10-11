package nodefabric

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 节点目录纪元（00155）的回执触发器只在「最近一次回执是否属于失败类」翻转时推进；失败类必须与
// 读方 RuntimeFailingSQL 同一份，否则读方认为变了、纪元却没动（订阅里的降级标记要等 TTL）。
// 改失败类时两边一起改：触发器函数要在新迁移里 CREATE OR REPLACE。
func TestNodeCatalogApplicationPhasesMatchRuntimeFailing(t *testing.T) {
	inList := regexp.MustCompile(`IN \(('[a-z_]+'(?:,\s*'[a-z_]+')*)\)`)
	phases := func(list string) string {
		var out []string
		for _, p := range strings.Split(list, ",") {
			out = append(out, strings.Trim(strings.TrimSpace(p), "'"))
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	m := inList.FindStringSubmatch(RuntimeFailingSQL("n"))
	if m == nil {
		t.Fatal("RuntimeFailingSQL no longer lists the failing phases with IN (...)")
	}
	want := phases(m[1])

	files, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	sort.Strings(files)
	var body, name string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		if i := strings.Index(up, "FUNCTION app.bump_node_catalog_on_application()"); i >= 0 {
			rest := up[i:]
			if j := strings.Index(rest, "$$;"); j >= 0 {
				rest = rest[:j]
			}
			body, name = rest, filepath.Base(f)
		}
	}
	if body == "" {
		t.Fatal("no migration defines app.bump_node_catalog_on_application()")
	}
	lists := inList.FindAllStringSubmatch(body, -1)
	if len(lists) != 2 {
		t.Fatalf("%s: want the failing phases listed twice (previous and new receipt), found %d", name, len(lists))
	}
	for _, l := range lists {
		if got := phases(l[1]); got != want {
			t.Fatalf("%s lists failing phases %s, RuntimeFailingSQL has %s", name, got, want)
		}
	}
}
