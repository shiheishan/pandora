// [INPUT]: 依赖本包 collectSQL、multisetDiff
// [OUTPUT]: 对外提供 TestSQLSetSurvivesMoveAcrossPackages、TestSQLSetCatchesEditedSQL
// [POS]: tools/refactorcheck 的 sqlset 自测：SQL 从 handler 挪到 domain、换缩进、拼接片段原样保留判相同；改一个字、struct 标签不算 SQL

package main

import (
	"sort"
	"strings"
	"testing"
)

const sqlHandler = `package admin

type resp struct {
	Name string ` + "`json:\"name\"`" + `
}

func list() {
	q := ` + "`SELECT id, name\n\t\tFROM app.pools\n\t\tWHERE tenant_id = $1`" + `
	if all {
		q += " AND status = $2"
	}
	_ = "pool not found"
}
`

const sqlDomain = `package nodefabric

func (s *Service) ListPools() {
	q := ` + "`SELECT id, name FROM app.pools WHERE tenant_id = $1`" + `
	if all {
		q += " AND status = $2"
	}
}
`

func sqlOf(t *testing.T, files map[string]string) []string {
	t.Helper()
	var all []sqlSite
	for name, src := range files {
		sites, err := collectSQL(name, []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, sites...)
	}
	return sortedSQL(all)
}

func TestSQLSetSurvivesMoveAcrossPackages(t *testing.T) {
	base := sqlOf(t, map[string]string{"api/admin/pools.go": sqlHandler})
	if len(base) != 2 {
		t.Fatalf("want the query and its fragment, got %q", base)
	}
	head := sqlOf(t, map[string]string{
		"api/admin/pools.go":         "package admin\n\nfunc list() { _ = \"pool not found\" }\n",
		"domain/nodefabric/pools.go": sqlDomain,
	})
	if a, b := multisetDiff(base, head); len(a)+len(b) != 0 {
		t.Fatalf("moved SQL reported as changed: only base %q, only head %q", a, b)
	}
}

func TestSQLSetCatchesEditedSQL(t *testing.T) {
	base := sqlOf(t, map[string]string{"p.go": sqlDomain})
	edited := sqlOf(t, map[string]string{"p.go": strings.Replace(sqlDomain, "status = $2", "status = $3", 1)})
	dup := sqlOf(t, map[string]string{"a.go": sqlDomain, "b.go": sqlDomain})
	for name, head := range map[string][]string{"edited fragment": edited, "duplicated query": dup} {
		sort.Strings(head)
		if a, b := multisetDiff(base, head); len(a)+len(b) == 0 {
			t.Errorf("%s: not reported", name)
		}
	}
}
