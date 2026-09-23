package node

import (
	"io"
	"log/slog"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
)

// testLogger 丢弃所有输出：这些测试关心的是返回值，不是日志内容。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// 面板下发的协议是权威，节点端要跟着切。
//
// 这条守的是「不用手动碰节点端」这个前提。原先 syncConfig 写死用本地
// config.json 里的 node_type，管理员在面板上把节点从 shadowsocks 改成
// vless，端口和参数都跟着变了、协议却没变——节点端拿 ss 的适配器去解析
// vless 的配置。要恢复只能上服务器改文件重启。
func TestProtocolFollowsPanel(t *testing.T) {
	client := panel.New(panel.Options{
		BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "shadowsocks", Token: "t",
	})
	n := New(client, nil, testLogger())

	got := n.protocolFrom(map[string]any{"protocol": "vless"})
	if got != "vless" {
		t.Errorf("协议 = %q，期望跟随面板下发的 vless", got)
	}
	// 切换后要记回客户端，否则每轮都重新切一次、面板那边也一直记
	// 「协议不一致」
	if client.NodeType() != "vless" {
		t.Errorf("客户端仍是 %q，没有记住新协议", client.NodeType())
	}
}

// 面板没下发 protocol 时回落到本地值。
//
// 老版本面板不带这个字段，回落让节点至少还能按原协议服务，而不是因为
// 读不到就整个起不来。
func TestProtocolFallsBackWhenPanelSilent(t *testing.T) {
	client := panel.New(panel.Options{
		BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "trojan", Token: "t",
	})
	n := New(client, nil, testLogger())

	for name, cfg := range map[string]map[string]any{
		"没有 protocol 字段": {"server_port": 443},
		"protocol 是空串":   {"protocol": ""},
		"protocol 只有空格":  {"protocol": "   "},
		"protocol 类型不对":  {"protocol": 123},
	} {
		if got := n.protocolFrom(cfg); got != "trojan" {
			t.Errorf("%s：得到 %q，期望回落到本地的 trojan", name, got)
		}
	}
	if client.NodeType() != "trojan" {
		t.Errorf("回落时不该改动客户端，现在是 %q", client.NodeType())
	}
}
