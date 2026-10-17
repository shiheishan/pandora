package render

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseTypes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"all", nil},
		{"ALL", nil},
		{"unknown", nil},
		{"vless|ss,hysteria", []string{"vless", "shadowsocks", "hysteria2"}},
		{"hy2|hysteria2", []string{"hysteria2"}},
		{"VLESS｜Trojan，tuic", []string{"vless", "trojan", "tuic"}},
		{strings.Repeat("vless|", 40), nil},
	}
	for _, c := range cases {
		got := ParseTypes(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseTypes(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestParseKeywords(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"香港|JP", []string{"香港", "jp"}},
		{strings.Repeat("香", 21), nil},
		{strings.Repeat("香", 20), []string{strings.Repeat("香", 20)}},
	}
	for _, c := range cases {
		got := ParseKeywords(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseKeywords(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestProtocolPrefix(t *testing.T) {
	nodes := []Node{
		{Name: "香港 01", Type: "vless", Host: "node.example.com", Port: 443, Config: map[string]any{}},
		{Name: " 东京 ", Type: "hysteria2", Host: "node.example.com", Port: 443, Config: map[string]any{}},
	}
	origName := nodes[0].Name
	rc := prepare(Request{Format: FormatClash, Nodes: nodes, ProtocolPrefix: true})
	if rc.nodes[0].Name != "[vless]香港 01" {
		t.Fatalf("节点 0 名字: %q", rc.nodes[0].Name)
	}
	if rc.nodes[1].Name != "[Hy2]东京" {
		t.Fatalf("节点 1 名字: %q", rc.nodes[1].Name)
	}
	if nodes[0].Name != origName {
		t.Fatalf("传入切片被改: %q", nodes[0].Name)
	}
}

func TestLineSafeNameCommentPrefix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"#01 香港", "01 香港"},
		{"; 东京", "东京"},
		{"// 新加坡", "新加坡"},
		{"## ;// x", "x"},
		{"a#b", "a#b"},
		{"a=b,c", "a b c"},
		{"[HK] 01", "【HK】 01"},
	}
	for _, c := range cases {
		if got := lineSafeName(c.in); got != c.want {
			t.Errorf("lineSafeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSurfboardCommentNameReferenced(t *testing.T) {
	n := Node{
		Name: "#01 香港", Type: "shadowsocks", Host: "node.example.com", Port: 8388,
		Config: map[string]any{"method": "aes-128-gcm"},
	}

	sbBody, _, _ := RenderRequest(Request{
		Format: FormatSurfboard, Nodes: []Node{n}, ProxyUUID: fixtureUUID,
	})
	proxyNames := surfboardProxyNames(string(sbBody))
	for name := range proxyNames {
		if strings.HasPrefix(name, "#") || strings.HasPrefix(name, ";") || strings.HasPrefix(name, "//") {
			t.Fatalf("[Proxy] 段含注释式名字: %q", name)
		}
	}
	if !proxyNames["01 香港"] {
		t.Fatalf("[Proxy] 段应有去掉 # 后的名字「01 香港」，集合=%v", proxyNames)
	}
	autoRefs := surfboardAutoSelectRefs(string(sbBody))
	if len(autoRefs) == 0 {
		t.Fatalf("没找到自动选择组的引用:\n%s", sbBody)
	}
	for _, ref := range autoRefs {
		if !proxyNames[ref] {
			t.Fatalf("自动选择引用了 [Proxy] 里没有的名字: %q，集合=%v", ref, proxyNames)
		}
	}

	loonBody, _, _ := RenderRequest(Request{
		Format: FormatLoon, Nodes: []Node{n}, ProxyUUID: fixtureUUID,
	})
	wantLoon := "01 香港 = Shadowsocks,"
	if !strings.HasPrefix(strings.TrimSpace(string(loonBody)), wantLoon) {
		t.Fatalf("Loon 首行应以 %q 开头，得到:\n%s", wantLoon, string(loonBody))
	}
}

// surfboardSection 返回 [section] 段内行（不含段标题行），到下一个 [ 段或文件末尾。
func surfboardSection(body, section string) []string {
	title := "[" + section + "]"
	lines := strings.Split(body, "\n")
	var in bool
	var out []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == title {
			in = true
			continue
		}
		if in {
			if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
				break
			}
			if trim != "" {
				out = append(out, trim)
			}
		}
	}
	return out
}

func surfboardProxyNames(body string) map[string]bool {
	names := map[string]bool{}
	for _, line := range surfboardSection(body, "Proxy") {
		idx := strings.Index(line, " = ")
		if idx < 0 {
			continue
		}
		names[strings.TrimSpace(line[:idx])] = true
	}
	return names
}

func surfboardAutoSelectRefs(body string) []string {
	for _, line := range surfboardSection(body, "Proxy Group") {
		if !strings.HasPrefix(line, "自动选择 = url-test, ") {
			continue
		}
		rest := strings.TrimPrefix(line, "自动选择 = url-test, ")
		parts := strings.Split(rest, ", ")
		var refs []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" || strings.HasPrefix(p, "url=") || strings.HasPrefix(p, "interval=") {
				continue
			}
			refs = append(refs, p)
		}
		return refs
	}
	return nil
}
