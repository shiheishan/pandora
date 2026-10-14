package node

import (
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
)

// startedNode 起一个 Server、连上、装好 n1（端口 20001）。
func startedNode(t *testing.T, kernel core.Core) *testServer {
	t.Helper()
	ts := newTestServer(t, kernel, "")
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001))
	return ts
}

// 全量按用户号 diff：只动变了的人，回 ok，Hello 快照跟着版本走。
func TestUsersFullDiffAndAck(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	ts.usersFull("n1", "u1-a", "a", "b")
	assertStrings(t, "内核", k.uuids("n1"), "a", "b")
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok")
	if _, users := ts.Applied(); len(users) != 1 || users[0].Version != "u1-a" {
		t.Fatalf("Hello 快照 = %v", users)
	}
	ts.handleEventUsers("n1", "u1-b", protoUser(2, "b"), protoUser(3, "c"))
	assertStrings(t, "内核", k.uuids("n1"), "b", "c")
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok", "u1-b:ok")
}

// handleEventUsers 下发一份用户号自定的全量。
func (ts *testServer) handleEventUsers(nodeID, version string, users ...*nodev1.User) {
	ts.handleEvent(usersFullEvent(nodeID, version, users))
}

// 增量：基准对得上才打；对不上、节点不存在、版本为空都回 version_mismatch，内核不动。
func TestUsersDeltaVersionCheck(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	ts.handleEventUsers("n1", "u1-b", protoUser(2, "b"), protoUser(3, "c"))
	ts.usersDelta("n1", "u1-b", "u1-c", []*nodev1.User{protoUser(4, "d")}, 2)
	assertStrings(t, "内核", k.uuids("n1"), "c", "d")
	ts.usersDelta("n1", "u1-x", "u1-y", []*nodev1.User{protoUser(5, "e")})
	assertStrings(t, "内核", k.uuids("n1"), "c", "d")
	if v := ts.node(t, "n1").userVersion; v != "u1-c" {
		t.Fatalf("版本 = %s", v)
	}
	ts.usersDelta("nx", "u1-c", "u1-d", nil, 3)
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-b:ok", "u1-c:ok", "u1-y:version_mismatch")
	assertStrings(t, "不存在的节点", ts.link.usersAcksOf("nx"), "u1-d:version_mismatch")
}

// 装不进内核：回 apply_failed、作废版本，之后的增量一律 version_mismatch。
func TestUsersApplyFailure(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	k.failAddUsers = 1
	ts.usersFull("n1", "u1-a", "a")
	if v := ts.node(t, "n1").userVersion; v != "" {
		t.Fatalf("版本没作废：%s", v)
	}
	ts.usersDelta("n1", "u1-a", "u1-b", []*nodev1.User{protoUser(2, "b")})
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:apply_failed", "u1-b:version_mismatch")
}

// 换配置重建入站：内核里现有的名单随新入站一起装（来源 known），版本不变、不另外回执。
func TestRebuildCarriesKnownUsers(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	ts.usersFull("n1", "u1-a", "a", "b")
	ts.config(portConfig(t, "n1", "vless", 20002))
	assertStrings(t, "重建后内核", k.uuids("n1"), "a", "b")
	if v := ts.node(t, "n1").userVersion; v != "u1-a" {
		t.Fatalf("版本 = %s", v)
	}
	if last := k.preload[len(k.preload)-1]; len(last) != 2 {
		t.Fatalf("重建时随入站装的名单 = %v", last)
	}
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok")
}

// 回滚也失败：节点停、镜像作废并回 resync；之后的全量先存着，入站起来时一起装并回 ok。
func TestRollbackFailureThenStagedUsers(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	ts.usersFull("n1", "u1-a", "a")
	k.failAddInbound = 2
	ts.config(portConfig(t, "n1", "vless", 20002))
	n := ts.node(t, "n1")
	if n.started || len(n.known) != 0 || n.runtime().Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN {
		t.Fatal("回滚失败后应停、镜像作废")
	}
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok", "u1-a:resync")

	ts.usersFull("n1", "u1-b", "a", "b")
	if n.staged == nil || len(ts.link.usersAcksOf("n1")) != 2 {
		t.Fatal("入站没起来时全量应先存着、不回执")
	}
	ts.config(portConfig(t, "n1", "vless", 20003))
	assertStrings(t, "内核", k.uuids("n1"), "a", "b")
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok", "u1-a:resync", "u1-b:ok")
	if n.userVersion != "u1-b" || n.staged != nil {
		t.Fatalf("版本 %s，staged %v", n.userVersion, n.staged)
	}
}

// 先有名单、再开 accept：配置之前到的全量随入站一起交给内核，之后不再单独 AddUsers。
func TestUsersBeforeConfigInstalledWithInbound(t *testing.T) {
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.usersFull("n1", "u1-a", "a", "b")
	ts.config(portConfig(t, "n1", "vless", 20001))
	if len(k.preload) != 1 || len(k.preload[0]) != 2 {
		t.Fatalf("随入站装的名单 = %v", k.preload)
	}
	assertStrings(t, "内核", k.uuids("n1"), "a", "b")
	assertStrings(t, "回执", ts.link.usersAcksOf("n1"), "u1-a:ok")
	if _, users := ts.Applied(); len(users) != 1 || users[0].Version != "u1-a" {
		t.Fatalf("Hello 快照 = %v", users)
	}
}

// 冷启动：落盘的配置与名单在会话之前装上，Hello 报缓存版本；Welcome 之前报 serving_cached_config。
func TestColdStartFromCacheWithUsers(t *testing.T) {
	dir := t.TempDir()
	first := newTestServer(t, newFakeKernel(), dir)
	first.welcome()
	nc := portConfig(t, "n1", "vless", 20001)
	first.config(nc)
	first.usersFull("n1", "u1-a", "a", "b")
	first.report()

	k := newFakeKernel()
	ts := newTestServer(t, k, dir)
	ts.Start()
	if len(k.preload) != 1 || len(k.preload[0]) != 2 {
		t.Fatalf("冷启动随入站装的名单 = %v", k.preload)
	}
	configs, users := ts.Applied()
	if len(configs) != 1 || configs[0].ContentSha256 != nc.ContentSha256 || len(users) != 1 || users[0].Version != "u1-a" {
		t.Fatalf("Hello 快照 = %v %v", configs, users)
	}
	if rt := ts.node(t, "n1").runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != reasonServingCached {
		t.Fatalf("冷启动状态 = %v", rt)
	}
	ts.welcome()
	if rt := ts.node(t, "n1").runtime(); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING {
		t.Fatalf("Welcome 后状态 = %v", rt)
	}
}

// 没连着时名单照常装进内核，不回执。
func TestUsersWhileDisconnected(t *testing.T) {
	k := newFakeKernel()
	ts := startedNode(t, k)
	ts.disconnect()
	ts.usersFull("n1", "u1-a", "a")
	assertStrings(t, "内核", k.uuids("n1"), "a")
	if got := ts.link.usersAcksOf("n1"); len(got) != 0 {
		t.Fatalf("没连着时回执了：%v", got)
	}
}
