package node

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

func runtimeOf(t *testing.T, ts *testServer, id string) *nodev1.NodeRuntime {
	t.Helper()
	return ts.node(t, id).runtime()
}

// 全量配置按列表装；同一份再发不重装；回执每条流各报一次。
func TestServerConfigInstallsAndAcksOncePerStream(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	n1, n2 := portConfig(t, "n1", "vless", 20001), portConfig(t, "n2", "trojan", 20002)
	ts.config(n1, n2)
	if k.port("n1") != 20001 || k.port("n2") != 20002 || k.protocol("n1") != "vless" || k.protocol("n2") != "trojan" {
		t.Fatalf("入站没按配置装上：%d %d %s %s", k.port("n1"), k.port("n2"), k.protocol("n1"), k.protocol("n2"))
	}
	sw1 := "CONFIG_PHASE_SWITCHED:" + n1.ContentSha256[:8]
	assertStrings(t, "n1 回执", ts.link.acks("n1"), sw1)
	configs, _ := ts.Applied()
	if len(configs) != 2 || configs[0].ContentSha256 != n1.ContentSha256 || configs[1].NodeId != "n2" {
		t.Fatalf("Hello 快照 = %v", configs)
	}

	calls := k.addInboundCalls
	ts.config(n1, n2)
	if k.addInboundCalls != calls {
		t.Fatal("同一份配置被重装了")
	}
	assertStrings(t, "同一条流上再发同一份", ts.link.acks("n1"), sw1)

	ts.disconnect()
	ts.welcome()
	ts.config(n1, n2)
	if k.addInboundCalls != calls {
		t.Fatal("重连后同一份配置被重装了")
	}
	assertStrings(t, "新流补报", ts.link.acks("n1"), sw1, sw1)
}

// health_passed 要切换后稳定 5 秒、内核就绪才报，只报一次。
func TestHealthPassedAfterStabilityWindow(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	ts.welcome()
	nc := portConfig(t, "n1", "vless", 20001)
	ts.config(nc)
	ts.clock.advance(4 * time.Second)
	ts.housekeeping()
	if got := ts.link.acks("n1"); len(got) != 1 {
		t.Fatalf("稳定窗口内就报了：%v", got)
	}
	ts.clock.advance(time.Second)
	ts.housekeeping()
	ts.housekeeping()
	hp := "CONFIG_PHASE_HEALTH_PASSED:" + nc.ContentSha256[:8]
	assertStrings(t, "回执", ts.link.acks("n1"), "CONFIG_PHASE_SWITCHED:"+nc.ContentSha256[:8], hp)
}

// 不在列表里的节点被停掉，最后一轮流量先收进缓冲。
func TestNodeMissingFromConfigIsRemoved(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001), portConfig(t, "n2", "vless", 20002))
	k.feed("n2", trafficOf(7, 100, 200))
	ts.config(portConfig(t, "n1", "vless", 20001))
	if k.port("n2") != 0 {
		t.Fatal("n2 的入站没删")
	}
	if _, ok := ts.nodes["n2"]; ok {
		t.Fatal("n2 还在节点表里")
	}
	if ts.traffic.size() != 1 {
		t.Fatalf("n2 最后一轮流量没进缓冲：%d", ts.traffic.size())
	}
	assertStrings(t, "顺序", ts.order, "n1")
}

// 摘要不符、不是 JSON 对象：报 failed 与对应原因码，节点没起。
func TestBadDocumentsReportFailed(t *testing.T) {
	ts := newTestServer(t, newFakeKernel(), "")
	ts.welcome()
	bad := portConfig(t, "n1", "vless", 20001)
	bad.ContentSha256 = strings.Repeat("a", 64)
	ts.config(bad)
	assertStrings(t, "摘要不符", ts.link.acks("n1"), "CONFIG_PHASE_FAILED:aaaaaaaa:digest_mismatch")
	if rt := runtimeOf(t, ts, "n1"); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN {
		t.Fatalf("状态 = %v", rt.Status)
	}

	arr := []byte("[]")
	ts.config(&nodev1.NodeConfig{NodeId: "n2", NodeType: "vless", ConfigJson: arr, ContentSha256: sha(arr)})
	assertStrings(t, "不是对象", ts.link.acks("n2"), "CONFIG_PHASE_FAILED:"+sha(arr)[:8]+":invalid_json")
}

// 坏版本、旧配置在服务：回滚继续服务；同版本重发不重装只补报；到点重试成功。
func TestBadVersionLedgerWhileServing(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	v1, v2 := portConfig(t, "n1", "vless", 20001), portConfig(t, "n1", "vless", 20002)
	ts.config(v1)
	k.failAddInbound = 1
	ts.config(v2)
	failed := "CONFIG_PHASE_FAILED:" + v2.ContentSha256[:8] + ":apply_failed"
	assertStrings(t, "回执", ts.link.acks("n1"), "CONFIG_PHASE_SWITCHED:"+v1.ContentSha256[:8], failed)
	if k.port("n1") != 20001 {
		t.Fatalf("旧配置没在服务：端口 %d", k.port("n1"))
	}
	if rt := runtimeOf(t, ts, "n1"); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != reasonApplyFailed {
		t.Fatalf("状态 = %v %s", rt.Status, rt.Reason)
	}

	calls := k.addInboundCalls
	ts.config(v2)
	if k.addInboundCalls != calls {
		t.Fatal("没到重试的点就重装了坏版本")
	}
	if got := ts.link.acks("n1"); len(got) != 2 {
		t.Fatalf("同一条流重复报 failed：%v", got)
	}
	ts.welcome()
	ts.config(v2)
	if k.addInboundCalls != calls {
		t.Fatal("重连后重装了坏版本")
	}
	if got := ts.link.acks("n1"); len(got) != 3 || got[2] != failed {
		t.Fatalf("新流没补报 failed：%v", got)
	}

	ts.clock.advance(67 * time.Second)
	ts.housekeeping()
	if k.port("n1") != 20002 || ts.node(t, "n1").failed != nil {
		t.Fatalf("到点重试没成功：端口 %d", k.port("n1"))
	}
	if got := ts.link.acks("n1"); got[len(got)-1] != "CONFIG_PHASE_SWITCHED:"+v2.ContentSha256[:8] {
		t.Fatalf("重试成功没报 switched：%v", got)
	}
	if rt := runtimeOf(t, ts, "n1"); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING {
		t.Fatalf("状态 = %v", rt.Status)
	}
}

// 节点已停（首次就没装上）时每分钟重试。
func TestStoppedNodeRetriesEveryMinute(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	k.failAddInbound = 1
	nc := portConfig(t, "n1", "vless", 20001)
	ts.config(nc)
	if rt := runtimeOf(t, ts, "n1"); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN {
		t.Fatalf("状态 = %v", rt.Status)
	}
	ts.clock.advance(30 * time.Second)
	ts.housekeeping()
	if k.port("n1") != 0 {
		t.Fatal("没到点就重试了")
	}
	ts.clock.advance(40 * time.Second)
	ts.housekeeping()
	if k.port("n1") != 20001 {
		t.Fatal("到点没重试")
	}
}

// 同机端口冲突按 ServerConfig 顺序先到先得，输家报占用者节点 id。
func TestPortConflictFollowsConfigOrder(t *testing.T) {
	for _, order := range [][2]string{{"n1", "n2"}, {"n2", "n1"}} {
		k := newFakeKernel()
		ts := newTestServer(t, k, "")
		ts.welcome()
		ts.config(portConfig(t, order[0], "vless", 20001), portConfig(t, order[1], "trojan", 20001))
		winner, loser := order[0], order[1]
		if k.port(winner) != 20001 || k.port(loser) != 0 {
			t.Fatalf("顺序 %v：赢家不对", order)
		}
		acks := ts.link.acks(loser)
		if len(acks) != 1 || !strings.Contains(acks[0], ":port_in_use:20001/tcp:"+winner) {
			t.Fatalf("顺序 %v：输家回执 %v", order, acks)
		}
		rt := runtimeOf(t, ts, loser)
		if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN || rt.Reason != "port_in_use:20001/tcp" || rt.ReasonNodeId != winner {
			t.Fatalf("顺序 %v：输家状态 %v", order, rt)
		}
	}
}

// 换协议跟着面板走；新协议装不上时按旧协议装回。
func TestProtocolSwitchAndRollback(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001))
	ts.config(nodeConfig(t, "n1", "trojan", map[string]any{"server_port": 20001, "x": 1}))
	if k.protocol("n1") != "trojan" {
		t.Fatalf("协议没跟着面板切：%s", k.protocol("n1"))
	}
	k.failAddInbound = 1
	ts.config(nodeConfig(t, "n1", "shadowsocks", map[string]any{"server_port": 20001, "x": 2}))
	if k.protocol("n1") != "trojan" || ts.node(t, "n1").nodeType != "trojan" {
		t.Fatalf("回滚没按旧协议装回：%s", k.protocol("n1"))
	}
}

// 缺 node_id 的跳过；重复的只认第一份。
func TestMalformedServerConfigEntries(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	first := portConfig(t, "n1", "vless", 20001)
	ts.config(portConfig(t, "", "vless", 20009), first, portConfig(t, "n1", "vless", 20002))
	if k.port("n1") != 20001 || len(ts.nodes) != 1 {
		t.Fatalf("端口 %d，节点数 %d", k.port("n1"), len(ts.nodes))
	}
	assertStrings(t, "顺序", ts.order, "n1")
}

// 没连着时照常装，不回执；连上后面板重发同一份才补报。
func TestConfigWhileDisconnected(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	nc := portConfig(t, "n1", "vless", 20001)
	ts.config(nc)
	if k.port("n1") != 20001 || len(ts.link.acks("n1")) != 0 {
		t.Fatal("没连着时应装上、不回执")
	}
	ts.welcome()
	ts.config(nc)
	assertStrings(t, "回执", ts.link.acks("n1"), "CONFIG_PHASE_SWITCHED:"+nc.ContentSha256[:8])
}

// 退避：1、2、4 分钟，之后封顶 5 分钟，±10%。
func TestApplyRetryDelaySchedule(t *testing.T) {
	for attempts, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 5 * time.Minute, 10: 5 * time.Minute} {
		for range 20 {
			got := applyRetryDelay(attempts)
			if got < want*9/10 || got > want*11/10 {
				t.Fatalf("第 %d 次：%s 不在 %s ±10%% 内", attempts, got, want)
			}
		}
	}
}

// 回执 detail 按 UTF-8 字符边界截到 2KB。
func TestReportDetailTruncatesOnRuneBoundary(t *testing.T) {
	got := reportDetail(strings.Repeat("汉", 3000))
	if len(got) > maxReportDetailBytes || !utf8.ValidString(got) {
		t.Fatalf("长度 %d，合法 UTF-8 %v", len(got), utf8.ValidString(got))
	}
	if reportDetail("short") != "short" {
		t.Fatal("短的不该截")
	}
}
