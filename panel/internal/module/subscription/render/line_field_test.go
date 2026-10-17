package render

import (
	"strings"
	"testing"
)

func TestLoonSurfboardSkipNewlineField(t *testing.T) {
	orig := nodeByName(t, "hy2-obfs")
	good := nodeByName(t, "ss-aes128")

	bad := orig
	bad.Config = map[string]any{}
	for k, v := range orig.Config {
		bad.Config[k] = v
	}
	bad.Config["server_name"] = "sni.example.com\nfoo = bar"

	loonLine, loonReason := nodeToLoon(bad, fixtureUUID)
	if loonLine != "" {
		t.Fatalf("Loon 应跳过含换行字段的节点，got line: %q", loonLine)
	}
	if !strings.Contains(loonReason, "换行") {
		t.Fatalf("Loon 跳过原因应含「换行」，got: %q", loonReason)
	}

	sbLine, sbReason := nodeToSurfboard(bad, fixtureUUID)
	if sbLine != "" {
		t.Fatalf("Surfboard 应跳过含换行字段的节点，got line: %q", sbLine)
	}
	if !strings.Contains(sbReason, "换行") {
		t.Fatalf("Surfboard 跳过原因应含「换行」，got: %q", sbReason)
	}

	for _, format := range []Format{FormatLoon, FormatSurfboard} {
		body, _, count := RenderRequest(Request{
			Format:    format,
			Nodes:     []Node{bad, good},
			ProxyUUID: fixtureUUID,
		})
		if count != 1 {
			t.Fatalf("%s: 应只渲染 1 个真实节点，count=%d", format, count)
		}
		if strings.Contains(string(body), "foo = bar") {
			t.Fatalf("%s: 正文不应含被跳过节点拆出的伪配置行:\n%s", format, string(body))
		}
	}

	check := nodeByName(t, "hy2-obfs")
	if check.Config["server_name"] != "sni.example.com" {
		t.Fatalf("夹具 hy2-obfs 的 server_name 不应被测试改动，got %v", check.Config["server_name"])
	}
}
