package node

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
)

// 日志里的 type 要跟着面板下发的协议走。原先构造时把它写死成本地 config.json 的
// 协议，面板把节点从 shadowsocks 换成 vless 之后，日志仍然报 shadowsocks。
func TestLogTypeFollowsProtocolSwitch(t *testing.T) {
	var buf bytes.Buffer
	client := panel.New(panel.Options{BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "shadowsocks", Token: "t"})
	n := New(client, nil, slog.New(slog.NewJSONHandler(&buf, nil)))

	last := func() map[string]any {
		t.Helper()
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		var rec map[string]any
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", lines[len(lines)-1], err)
		}
		return rec
	}

	n.log.Info("切换前")
	if rec := last(); rec["type"] != "shadowsocks" || rec["node"] != "n1" {
		t.Fatalf("切换前日志 = %v，期望 node=n1 type=shadowsocks", rec)
	}
	n.protocolFrom(map[string]any{"protocol": "vless"})
	n.log.Info("切换后", "k", "v")
	rec := last()
	if rec["type"] != "vless" || rec["node"] != "n1" || rec["k"] != "v" {
		t.Fatalf("切换后日志 = %v，期望 node=n1 type=vless k=v", rec)
	}
	if strings.Count(buf.String(), `"type"`) != strings.Count(buf.String(), "\n") {
		t.Fatalf("每条日志恰好一个 type：%s", buf.String())
	}
}
