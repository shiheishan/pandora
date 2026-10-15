package render

import (
	"sort"
	"strings"
	"testing"
)

// fillTemplate 自己扫描替换，结果要与 strings.NewReplacer（键按长到短、等长按字典序排）逐字节一致。
func TestFillTemplateMatchesReplacer(t *testing.T) {
	vals := []map[string]string{
		{phProxies: "proxies:\n  - {name: \"a\"}", phProxyGroups: "  - {name: g}", phProxyNames: "\"a\", \"b\""},
		{phOutbounds: `{"tag":"{{proxy_tags}}"}`, phProxyTags: `"x", "y"`},
		{phProxies: "P{{proxy_names}}", phProxyNames: "", phSubscribeURL: "https://example.com/s/{{proxies}}"},
	}
	tmpls := []string{
		"",
		"no placeholders {{ here } \"quoted\"",
		defaultClashTemplate,
		"{{proxies}}{{proxies}}{{proxy_groups}}\n{{{proxy_names}}}",
		`["{{outbounds}}", "{{proxy_tags}}", {{outbounds}}, "{{proxy_tags}}"]`,
		"#!MANAGED-CONFIG {{subscribe_url}}\n{{proxies}}\n{{proxy_name",
		"中文{{proxy_names}}结尾{",
	}
	for vi, v := range vals {
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) > len(keys[j])
			}
			return keys[i] < keys[j]
		})
		pairs := make([]string, 0, 2*len(keys))
		for _, k := range keys {
			pairs = append(pairs, k, v[k])
		}
		ref := strings.NewReplacer(pairs...)
		for ti, tmpl := range tmpls {
			if got, want := string(fillTemplate(tmpl, v)), ref.Replace(tmpl); got != want {
				t.Errorf("vals#%d tmpl#%d:\n got %q\nwant %q", vi, ti, got, want)
			}
		}
	}
}

// splitInfo 只切片不复制：往信息节点那半 append 不能覆盖真实节点。
func TestSplitInfoAppendDoesNotClobber(t *testing.T) {
	rc := renderCtx{nodes: []Node{{Name: "i1", info: true}, {Name: "r1"}, {Name: "r2"}}}
	info, real := rc.splitInfo()
	if len(info) != 1 || len(real) != 2 || real[0].Name != "r1" {
		t.Fatalf("info=%v real=%v", info, real)
	}
	_ = append(info, Node{Name: "x"})
	if rc.nodes[1].Name != "r1" {
		t.Fatalf("append 到 info 覆盖了真实节点：%v", rc.nodes)
	}
}
