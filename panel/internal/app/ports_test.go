package app

import (
	"strings"
	"testing"
)

type fakePorts []string

func (f fakePorts) MissingPorts() []string { return f }

func TestCheckPortsAllWired(t *testing.T) {
	if err := checkPorts([]portCheck{{"a", fakePorts(nil)}, {"b", fakePorts{}}}); err != nil {
		t.Fatalf("全部装配齐时应返回 nil，得到 %v", err)
	}
}

func TestCheckPortsListsEveryGap(t *testing.T) {
	err := checkPorts([]portCheck{
		{"identity", fakePorts{"SessionRevoker", "PluginEvents"}},
		{"order", fakePorts(nil)},
		{"node", fakePorts{"Realtime"}},
	})
	if err == nil {
		t.Fatal("有缺口时应返回错误")
	}
	msg := err.Error()
	for _, want := range []string{"identity [SessionRevoker PluginEvents]", "node [Realtime]"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误里应列出 %q，得到 %q", want, msg)
		}
	}
	if strings.Contains(msg, "order") {
		t.Fatalf("装配齐的模块不应出现在错误里：%q", msg)
	}
}
