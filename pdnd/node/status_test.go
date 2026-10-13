package node

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// Welcome 立刻报一次心跳：本机资源加各节点运行状态，按面板下发顺序。
func TestHeartbeatOnWelcome(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.config(portConfig(t, "n2", "vless", 20002), portConfig(t, "n1", "trojan", 20002))
	ts.disconnect()
	ts.welcome()
	hb := ts.link.lastHeartbeat()
	if hb == nil || hb.GetMetrics().GetCpuBp() != testMetrics().GetCpuBp() {
		t.Fatalf("心跳 = %v", hb)
	}
	var got []string
	for _, rt := range hb.GetNodes() {
		got = append(got, fmt.Sprintf("%s:%s:%s:%s", rt.GetNodeId(), rt.GetStatus(), rt.GetReason(), rt.GetReasonNodeId()))
	}
	assertStrings(t, "节点状态", got,
		"n2:RUNTIME_STATUS_RUNNING::",
		"n1:RUNTIME_STATUS_DOWN:port_in_use:20002/tcp:n2")
}

// 没连着不采样、不发心跳。
func TestNoHeartbeatWhileDisconnected(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	ts.sendHeartbeat()
	ts.welcome()
	ts.disconnect()
	ts.sendHeartbeat()
	ts.link.mu.Lock()
	defer ts.link.mu.Unlock()
	if len(ts.link.heartbeats) != 1 {
		t.Fatalf("心跳 %d 次，期望只有 Welcome 那一次", len(ts.link.heartbeats))
	}
}

// 新配置装不上、旧配置仍在服务：DEGRADED，原因 config_apply_failed；面板确认之前靠缓存：serving_cached_config。
func TestRuntimeStates(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001))
	n := ts.node(t, "n1")
	if rt := n.runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING || rt.Reason != "" {
		t.Fatalf("装上后 = %v", rt)
	}
	k.failAddInbound = 1
	ts.config(portConfig(t, "n1", "vless", 20009))
	if rt := n.runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != reasonApplyFailed {
		t.Fatalf("新配置装不上时 = %v", rt)
	}
	fresh := &Node{id: "n9"}
	if rt := fresh.runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN || rt.Reason != reasonNotStarted {
		t.Fatalf("没起来的节点 = %v", rt)
	}
	cached := &Node{id: "n8", started: true, fromCache: true}
	if rt := cached.runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != reasonServingCached {
		t.Fatalf("靠缓存服务的节点 = %v", rt)
	}
}

func TestSplitPortReason(t *testing.T) {
	for _, tc := range []struct{ in, code, node string }{
		{"port_in_use:443/tcp:n7", "port_in_use:443/tcp", "n7"},
		{"port_in_use:443/udp:other", "port_in_use:443/udp", ""},
		{"port_in_use:443/tcp", "port_in_use:443/tcp", ""},
		{"config_apply_failed", "config_apply_failed", ""},
	} {
		code, node := splitPortReason(tc.in)
		if code != tc.code || node != tc.node {
			t.Errorf("splitPortReason(%q) = %q %q", tc.in, code, node)
		}
	}
}

// 内核原因只留可打印 ASCII 并截断；没有机器可读原因就用缺省。
func TestApplyErrReason(t *testing.T) {
	if got := applyErrReason(errors.New("x"), reasonNotStarted); got != reasonNotStarted {
		t.Fatalf("普通错误 = %q", got)
	}
	if got := applyErrReason(nil, reasonApplyFailed); got != reasonApplyFailed {
		t.Fatalf("nil = %q", got)
	}
	wrapped := fmt.Errorf("装不上: %w", &portInUseError{reason: "port_in_use:1/tcp:\n节点"})
	if got := applyErrReason(wrapped, reasonNotStarted); got != "port_in_use:1/tcp:" {
		t.Fatalf("包装的端口冲突 = %q", got)
	}
	if got := applyErrReason(&portInUseError{reason: "端口"}, reasonNotStarted); got != reasonNotStarted {
		t.Fatalf("全是非 ASCII 时应退回缺省，得到 %q", got)
	}
	if got := asciiReason(strings.Repeat("a", maxRuntimeReasonBytes+10)); len(got) != maxRuntimeReasonBytes {
		t.Fatalf("没截断：%d", len(got))
	}
}
