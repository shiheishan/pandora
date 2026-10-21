package node

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/panel"
)

// 落盘缓存读写：权限、往返、身份不符整个丢弃、摘要不符丢弃该文件。
func TestCacheRoundTripAndPermissions(t *testing.T) {
	root := t.TempDir()
	c := newServerCache(root, testServerID)
	must(t, c.saveOrder([]string{"n2", "n1"}))
	nc := portConfig(t, "n1", "vless", 20001)
	nCache := c.node("n1")
	must(t, nCache.saveConfig(nc))
	must(t, nCache.saveUsers("u1-a", map[int64]core.User{1: {ID: 1, UUID: "a", SpeedLimit: 5, DeviceLimit: 2}}))

	order, err := c.loadOrder()
	must(t, err)
	assertStrings(t, "顺序", order, "n2", "n1")
	got, err := nCache.loadConfig()
	must(t, err)
	if string(got.GetConfigJson()) != string(nc.GetConfigJson()) || got.GetContentSha256() != nc.GetContentSha256() || got.GetNodeType() != "vless" {
		t.Fatalf("读回的配置 = %v", got)
	}
	version, users, err := nCache.loadUsers()
	must(t, err)
	if version != "u1-a" || len(users) != 1 || users[0] != (core.User{ID: 1, UUID: "a", SpeedLimit: 5, DeviceLimit: 2}) {
		t.Fatalf("读回的名单 = %s %v", version, users)
	}
	for path, want := range map[string]os.FileMode{
		filepath.Join(root, "nodes"):                      0o700,
		filepath.Join(root, "nodes", "n1"):                0o700,
		filepath.Join(root, "nodes", "server.json"):       0o600,
		filepath.Join(root, "nodes", "n1", "config.json"): 0o600,
		filepath.Join(root, "nodes", "n1", "users.json"):  0o600,
	} {
		info, err := os.Stat(path)
		must(t, err)
		if info.Mode().Perm() != want {
			t.Errorf("%s 权限 %v，期望 %v", path, info.Mode().Perm(), want)
		}
	}

	// 另一个身份读：整个缓存目录丢弃。
	other := newServerCache(root, "00000000-0000-4000-8000-000000000002")
	if _, err := other.loadOrder(); err == nil {
		t.Fatal("别的身份的缓存应被拒")
	}
	if _, err := os.Stat(filepath.Join(root, "nodes")); !os.IsNotExist(err) {
		t.Fatal("别的身份的缓存应整个删掉")
	}
}

// 配置缓存的摘要对不上：丢弃这个文件。
func TestCachedConfigDigestMismatchDiscarded(t *testing.T) {
	c := newServerCache(t.TempDir(), testServerID).node("n1")
	must(t, c.saveConfig(portConfig(t, "n1", "vless", 20001)))
	body, err := os.ReadFile(c.configPath())
	must(t, err)
	var f map[string]any
	must(t, json.Unmarshal(body, &f))
	f["config_json"] = []byte(`{"server_port":20002}`)
	must(t, os.WriteFile(c.configPath(), mustJSON(t, f), 0o600))
	if _, err := c.loadConfig(); err == nil {
		t.Fatal("摘要不符的配置应被拒")
	}
	if _, err := os.Stat(c.configPath()); !os.IsNotExist(err) {
		t.Fatal("摘要不符的配置文件应删掉")
	}
}

func TestSafePathSegment(t *testing.T) {
	for _, id := range []string{"n1", "node_7.a-b"} {
		if got := safePathSegment(id); got != id {
			t.Errorf("safePathSegment(%q) = %q", id, got)
		}
	}
	for _, id := range []string{".", "..", "../x", "a/b", "", string(make([]byte, 65))} {
		got := safePathSegment(id)
		if len(got) != len("node-")+24 || got[:5] != "node-" {
			t.Errorf("safePathSegment(%q) = %q，应是哈希名", id, got)
		}
	}
}

// 面板下发：装上的配置与顺序落盘；不在列表里的节点连缓存一起删掉。
func TestServerCachesConfigAndRemovesGoneNodes(t *testing.T) {
	dir := t.TempDir()
	ts := newTestServer(t, newFakeKernel(), dir)
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001), portConfig(t, "n2", "trojan", 20002))
	c := newServerCache(dir, testServerID)
	order, err := c.loadOrder()
	must(t, err)
	assertStrings(t, "顺序", order, "n1", "n2")
	if _, err := c.node("n2").loadConfig(); err != nil {
		t.Fatalf("n2 的配置没落盘：%v", err)
	}
	ts.config(portConfig(t, "n1", "vless", 20001))
	if _, err := os.Stat(filepath.Join(dir, "nodes", "n2")); !os.IsNotExist(err) {
		t.Fatal("删掉的节点缓存还在")
	}
	order, err = c.loadOrder()
	must(t, err)
	assertStrings(t, "顺序", order, "n1")
}

// 冷启动按缓存顺序装：同机端口冲突时赢家与上次一致；坏掉的节点缓存跳过，其余照装。
func TestColdStartFollowsCachedOrder(t *testing.T) {
	dir := t.TempDir()
	c := newServerCache(dir, testServerID)
	must(t, c.saveOrder([]string{"n2", "n1", "n3"}))
	must(t, c.node("n1").saveConfig(portConfig(t, "n1", "vless", 20001)))
	must(t, c.node("n2").saveConfig(portConfig(t, "n2", "trojan", 20001)))
	k := newFakeKernel()
	ts := newTestServer(t, k, dir)
	ts.Start()
	if k.port("n2") != 20001 || k.port("n1") != 0 {
		t.Fatal("冷启动没按缓存顺序装")
	}
	if _, ok := ts.nodes["n3"]; ok {
		t.Fatal("没有配置缓存的节点不该建")
	}
	configs, _ := ts.Applied()
	if len(configs) != 1 || configs[0].GetNodeId() != "n2" {
		t.Fatalf("Hello 快照 = %v", configs)
	}
}

// ---------------------------------------------------------------------------
// 真 NativeCore：面板不可达时重启，入站照常监听、名单里的用户能认证
// ---------------------------------------------------------------------------

func newNativeKernel(t *testing.T) *nativekernel.NativeCore {
	t.Helper()
	k := nativekernel.NewNativeCoreWithLogger(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := k.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// socks5Auth 走一遍 SOCKS5 用户名/口令认证（pdnd 的 socks 入站两者都是用户 UUID），
// 返回认证是否通过。
func socks5Auth(t *testing.T, port int, uuid string) bool {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("入站没有在监听：%v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 2 {
		t.Fatalf("SOCKS5 方法协商失败：%v %v", reply, err)
	}
	req := []byte{1, byte(len(uuid))}
	req = append(req, uuid...)
	req = append(req, byte(len(uuid)))
	req = append(req, uuid...)
	if _, err := conn.Write(req); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return false
	}
	return reply[1] == 0
}

func TestServesFromCacheWhenPanelDown(t *testing.T) {
	dir := t.TempDir()
	port := freeTCPPort(t)
	nc := nodeConfig(t, "7", "socks", map[string]any{"server_port": port})

	firstKernel := newNativeKernel(t)
	first := newTestServer(t, firstKernel, dir)
	first.welcome()
	first.config(nc)
	first.usersFull("7", "u-1", "alice-uuid-0001", "bob-uuid-00002")
	first.report()
	if !socks5Auth(t, port, "alice-uuid-0001") {
		t.Fatal("面板正常时没起来")
	}
	// 「进程退出」：关掉内核，端口让出来。
	_ = firstKernel.Close()

	// 重启时面板不可达：只有冷启动，没有 Welcome。
	restartedKernel := newNativeKernel(t)
	restarted := newTestServer(t, restartedKernel, dir)
	restarted.Start()
	n := restarted.node(t, "7")
	if !n.started {
		t.Fatal("面板不可达时重启没用缓存起服务")
	}
	if !socks5Auth(t, port, "alice-uuid-0001") {
		t.Fatal("缓存起服务后已授权用户认证不过")
	}
	if socks5Auth(t, port, "mallory-uuid-999") {
		t.Fatal("缓存起服务后未授权用户也认证通过了")
	}
	if rt := n.runtime(time.Time{}); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED || rt.Reason != reasonServingCached {
		t.Fatalf("面板确认前状态 = %v", rt)
	}

	// 面板回来：Hello 报的就是缓存版本；同一版配置再下发不重建入站。
	configs, users := restarted.Applied()
	if len(configs) != 1 || configs[0].GetContentSha256() != nc.GetContentSha256() || len(users) != 1 || users[0].GetVersion() != "u-1" {
		t.Fatalf("Hello 快照 = %v %v", configs, users)
	}
	restarted.welcome()
	restarted.config(nc)
	if !socks5Auth(t, port, "bob-uuid-00002") {
		t.Fatal("面板回来后已授权用户认证不过")
	}
	if rt := n.runtime(time.Time{}); rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING {
		t.Fatalf("面板回来后状态 = %v", rt)
	}
	assertStrings(t, "回执", restarted.link.acks("7"), "CONFIG_PHASE_SWITCHED:"+nc.GetContentSha256()[:8])
}

// 身份失效：立刻停掉全部入站、删掉节点缓存；重启后冷启动也起不来任何节点。
func TestTombstoneStopsServingAndClearsCache(t *testing.T) {
	dir := t.TempDir()
	c := newServerCache(dir, testServerID)
	must(t, c.saveOrder([]string{"n1", "n2"}))
	must(t, c.node("n1").saveConfig(portConfig(t, "n1", "vless", 20001)))
	must(t, c.node("n2").saveConfig(portConfig(t, "n2", "trojan", 20002)))
	k := newFakeKernel()
	ts := newTestServer(t, k, dir)
	ts.Start()
	if k.port("n1") != 20001 || k.port("n2") != 20002 {
		t.Fatal("前提：两个节点先从缓存起来")
	}

	ts.handleEvent(panel.Event{Kind: panel.EventTombstone, Reason: "server_retired"})
	if k.port("n1") != 0 || k.port("n2") != 0 {
		t.Fatal("身份失效后入站还在")
	}
	if len(ts.nodes) != 0 || len(ts.order) != 0 {
		t.Fatalf("身份失效后还留着节点：%v", ts.order)
	}

	k2 := newFakeKernel()
	again := newTestServer(t, k2, dir)
	again.Start()
	if len(again.nodes) != 0 || k2.port("n1") != 0 {
		t.Fatal("重启后又靠缓存起了服务")
	}
}
