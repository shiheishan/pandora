package kernel

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

func vlessInbound(tag string, port int, network string) *core.InboundConfig {
	return &core.InboundConfig{
		Tag: tag, Protocol: "vless", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"network": network, "security": "none"},
	}
}

func assertPortInUse(t *testing.T, err error, port int, l4, owner string, preserved bool) {
	t.Helper()
	var inUse *PortInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("期望端口占用错误，得到 %v", err)
	}
	if inUse.Port != port || inUse.L4 != l4 || inUse.Owner != owner {
		t.Fatalf("占用错误 = %+v，期望 %d/%s 被 %q 占用", inUse, port, l4, owner)
	}
	var applyErr *core.ConfigApplyError
	if errors.As(err, &applyErr) && applyErr.PreviousPreserved != preserved {
		t.Fatalf("PreviousPreserved = %v，期望 %v", applyErr.PreviousPreserved, preserved)
	}
}

// 同机两个入站配到同一个 TCP 端口：先来的一直占着，后来的不启动，占着的那个
// 不被先关再起。原先由 goroutine 竞速决定谁 bind 成功，输家随机。
func TestPortClaimFirstComerKeepsPort(t *testing.T) {
	c := newTestNativeCore(t)
	port := reserveTCPPort(t)
	if err := c.ApplyInbound(vlessInbound("vless-a", port, "tcp"), nil); err != nil {
		t.Fatalf("先来的入站没起来：%v", err)
	}
	c.mu.RLock()
	first := c.inbounds["vless-a"]
	c.mu.RUnlock()

	err := c.ApplyInbound(vlessInbound("vless-b", port, "tcp"), nil)
	assertPortInUse(t, err, port, "tcp", "vless-a", false)
	want := "端口 " + strconv.Itoa(port) + "/TCP 已被节点 vless-a 占用"
	if err.Error() != want {
		t.Fatalf("错误文案 = %q，期望 %q", err.Error(), want)
	}
	var reason interface{ RuntimeReason() string }
	if !errors.As(err, &reason) || reason.RuntimeReason() != "port_in_use:"+strconv.Itoa(port)+"/tcp:vless-a" {
		t.Fatalf("机器可读原因不对：%v", err)
	}
	c.mu.RLock()
	still := c.inbounds["vless-a"]
	_, loserPublished := c.inbounds["vless-b"]
	c.mu.RUnlock()
	if still != first {
		t.Fatal("占着端口的入站被替换（先关再起）了")
	}
	if loserPublished {
		t.Fatal("撞端口的入站被发布了")
	}
	if c.InboundReady("vless-a") != nil {
		t.Fatal("占着端口的入站不再就绪")
	}
	assertPortServing(t, "tcp", port)

	// 先来的删掉之后，后来的就能起来——不用重启进程。
	if err := c.DelInbound("vless-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyInbound(vlessInbound("vless-b", port, "tcp"), nil); err != nil {
		t.Fatalf("占用者退场后仍起不来：%v", err)
	}
	if owner := c.PortOwner(port, "tcp"); owner != "vless-b" {
		t.Fatalf("端口归属 = %q，期望 vless-b", owner)
	}
}

// TCP 与 UDP 分开登记：同一个端口号 TCP、UDP 各被一个入站占用是合法的。
func TestPortClaimTCPAndUDPCoexist(t *testing.T) {
	c := newTestNativeCore(t)
	port := reserveTCPPort(t)
	if err := c.ApplyInbound(vlessInbound("vless-tcp", port, "tcp"), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyInbound(vlessInbound("vless-kcp", port, "mkcp"), nil); err != nil {
		t.Fatalf("同端口号的 UDP 入站被拦了：%v", err)
	}
	if c.PortOwner(port, "tcp") != "vless-tcp" || c.PortOwner(port, "udp") != "vless-kcp" {
		t.Fatalf("登记表不对：tcp=%q udp=%q", c.PortOwner(port, "tcp"), c.PortOwner(port, "udp"))
	}
	// 第三个 UDP 入站撞上来
	err := c.ApplyInbound(vlessInbound("vless-kcp2", port, "mkcp"), nil)
	assertPortInUse(t, err, port, "udp", "vless-kcp", false)
}

// 改端口：先登记新端口再释放旧端口；新端口被占就保留旧入站（PreviousPreserved）。
func TestPortClaimMoveKeepsOldWhenTargetTaken(t *testing.T) {
	c := newTestNativeCore(t)
	p1, p2, p3 := reserveTCPPort(t), reserveTCPPort(t), reserveTCPPort(t)
	if err := c.ApplyInbound(vlessInbound("vless-a", p1, "tcp"), nil); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyInbound(vlessInbound("vless-b", p2, "tcp"), nil); err != nil {
		t.Fatal(err)
	}
	err := c.ApplyInbound(vlessInbound("vless-a", p2, "tcp"), nil)
	assertPortInUse(t, err, p2, "tcp", "vless-b", true)
	if c.PortOwner(p1, "tcp") != "vless-a" || c.InboundReady("vless-a") != nil {
		t.Fatal("改端口失败后旧入站没有保留")
	}
	assertPortServing(t, "tcp", p1)

	if err := c.ApplyInbound(vlessInbound("vless-a", p3, "tcp"), nil); err != nil {
		t.Fatalf("改到空闲端口失败：%v", err)
	}
	if c.PortOwner(p1, "tcp") != "" || c.PortOwner(p3, "tcp") != "vless-a" {
		t.Fatalf("改端口成功后登记没跟上：p1=%q p3=%q", c.PortOwner(p1, "tcp"), c.PortOwner(p3, "tcp"))
	}
	if err := c.ApplyInbound(vlessInbound("vless-c", p1, "tcp"), nil); err != nil {
		t.Fatalf("旧端口释放后别的入站仍起不来：%v", err)
	}
}

// 端口被本机其他进程占着：报 external 占用；对方释放后同一份配置能装上。
func TestPortClaimExternalOccupantThenRelease(t *testing.T) {
	c := newTestNativeCore(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	err = c.ApplyInbound(vlessInbound("vless-a", port, "tcp"), nil)
	assertPortInUse(t, err, port, "tcp", "", false)
	var reason interface{ RuntimeReason() string }
	if !errors.As(err, &reason) || reason.RuntimeReason() != "port_in_use:"+strconv.Itoa(port)+"/tcp:external" {
		t.Fatalf("外部占用的机器可读原因不对：%v", err)
	}
	if c.PortOwner(port, "tcp") != "" {
		t.Fatal("没起来的入站在登记表里留了名")
	}
	_ = ln.Close()
	if err := c.ApplyInbound(vlessInbound("vless-a", port, "tcp"), nil); err != nil {
		t.Fatalf("外部占用释放后仍装不上：%v", err)
	}
}

// 并发抢同一个端口：恰好一个赢，其余都是占用错误，没有两个都起来或都没起来。
func TestPortClaimConcurrentApplyHasSingleWinner(t *testing.T) {
	c := newTestNativeCore(t)
	port := reserveTCPPort(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.ApplyInbound(vlessInbound("vless-"+strconv.Itoa(i), port, "tcp"), nil)
		}(i)
	}
	wg.Wait()
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			if c.PortOwner(port, "tcp") != "vless-"+strconv.Itoa(i) {
				t.Fatalf("赢家 vless-%d 不是登记表里的占用者 %q", i, c.PortOwner(port, "tcp"))
			}
			continue
		}
		var inUse *PortInUseError
		if !errors.As(err, &inUse) {
			t.Fatalf("输家的错误不是端口占用：%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("赢家 %d 个，期望恰好 1 个：%v", winners, errs)
	}
}

func TestInboundPortKeyDerivation(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		raw      map[string]any
		want     string
	}{
		{"vless", map[string]any{"network": "tcp"}, "tcp"},
		{"vless", map[string]any{"network": "xhttp-h3"}, "udp"},
		{"vmess", map[string]any{"network": "kcp"}, "udp"},
		{"trojan", map[string]any{"network": "mkcp"}, "udp"},
		{"trojan", map[string]any{"network": "ws"}, "tcp"},
		{"hysteria2", nil, "udp"},
		{"tuic", nil, "udp"},
		{"juicity", nil, "udp"},
		{"shadowsocks", map[string]any{"network": "udp"}, "udp"},
		{"ss", nil, "tcp"},
		{"mieru", map[string]any{"transport": "UDP"}, "udp"},
		{"mieru", nil, "tcp"},
		{"socks", nil, "tcp"},
		{"anytls", nil, "tcp"},
	} {
		got := inboundPortKey(&core.InboundConfig{Protocol: tc.protocol, Port: 443, Raw: tc.raw})
		if got.l4 != tc.want || got.port != 443 {
			t.Errorf("%s %v → %v，期望 443/%s", tc.protocol, tc.raw, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 退场入站的流量与关停时交出最后一轮
// ---------------------------------------------------------------------------

// trafficAdapter 是带流量计数的假适配器：Close 时把「在途连接」的流量入账，
// 模拟真适配器 Close 等连接 goroutine 收尾、TCP 流量在连接结束时才计入。
type trafficAdapter struct {
	mu       sync.Mutex
	traffic  map[int64]core.UserTraffic
	inflight []core.UserTraffic
}

func (a *trafficAdapter) Protocol() string                                       { return "fake" }
func (a *trafficAdapter) Validate(InboundSpec) error                             { return nil }
func (a *trafficAdapter) Start(context.Context, InboundSpec, AdapterHooks) error { return nil }
func (a *trafficAdapter) AddUsers([]core.User) error                             { return nil }
func (a *trafficAdapter) UpsertUsers([]core.User) error                          { return nil }
func (a *trafficAdapter) DelUsers([]string) error                                { return nil }
func (a *trafficAdapter) OnlineIPs() map[int64][]string                          { return nil }
func (a *trafficAdapter) add(t core.UserTraffic) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := a.traffic[t.ID]
	cur.ID = t.ID
	cur.Upload += t.Upload
	cur.Download += t.Download
	a.traffic[t.ID] = cur
}
func (a *trafficAdapter) Close() error {
	for _, t := range a.inflight {
		a.add(t)
	}
	a.inflight = nil
	return nil
}
func (a *trafficAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for _, t := range a.traffic {
		out = append(out, t)
	}
	a.traffic = make(map[int64]core.UserTraffic)
	return out, nil
}

func newTrafficCore(t *testing.T) (*NativeCore, *[]*trafficAdapter) {
	t.Helper()
	var adapters []*trafficAdapter
	registry := NewAdapterRegistry()
	if err := registry.Register("fake", func(InboundSpec) (Adapter, error) {
		a := &trafficAdapter{traffic: make(map[int64]core.UserTraffic)}
		adapters = append(adapters, a)
		return a, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewNativeCore(registry)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, &adapters
}

func sumTraffic(in []core.UserTraffic) (up, down int64) {
	for _, t := range in {
		up += t.Upload
		down += t.Download
	}
	return up, down
}

// 关停时先关入站再取流量：在途连接在 Close 时入账，CloseAndDrainTraffic 交得出来；
// 先取再关（原来的顺序）就丢了。
func TestCloseAndDrainTrafficIncludesInflightConnections(t *testing.T) {
	c, adapters := newTrafficCore(t)
	if err := c.ApplyInbound(&core.InboundConfig{Tag: "fake-1", Protocol: "fake", Port: 40001}, nil); err != nil {
		t.Fatal(err)
	}
	a := (*adapters)[0]
	a.add(core.UserTraffic{ID: 7, Upload: 100, Download: 200})
	a.inflight = []core.UserTraffic{{ID: 7, Upload: 1000, Download: 2000}}

	drained, err := c.CloseAndDrainTraffic()
	if err != nil {
		t.Fatal(err)
	}
	up, down := sumTraffic(drained["fake-1"])
	if up != 1100 || down != 2200 {
		t.Fatalf("关停交出的流量 = %d/%d，期望 1100/2200（含在途连接）", up, down)
	}
	again, _ := c.CloseAndDrainTraffic()
	if len(again) != 0 {
		t.Fatalf("重复关停又交出了流量：%v", again)
	}
}

// 入站换代（改配置）时旧适配器上的流量不丢，下一次 GetTraffic 一并交出。
func TestReplacedInboundTrafficCarriesOver(t *testing.T) {
	c, adapters := newTrafficCore(t)
	cfg := &core.InboundConfig{Tag: "fake-1", Protocol: "fake", Port: 40001}
	if err := c.ApplyInbound(cfg, nil); err != nil {
		t.Fatal(err)
	}
	(*adapters)[0].add(core.UserTraffic{ID: 1, Upload: 10, Download: 20})
	(*adapters)[0].inflight = []core.UserTraffic{{ID: 1, Upload: 5, Download: 5}}
	if err := c.ApplyInbound(&core.InboundConfig{Tag: "fake-1", Protocol: "fake", Port: 40002}, nil); err != nil {
		t.Fatal(err)
	}
	(*adapters)[1].add(core.UserTraffic{ID: 1, Upload: 1, Download: 1})
	got, err := c.GetTraffic("fake-1")
	if err != nil {
		t.Fatal(err)
	}
	if up, down := sumTraffic(got); up != 16 || down != 26 {
		t.Fatalf("换代后取到 %d/%d，期望 16/26（旧入站 15/25 + 新入站 1/1）", up, down)
	}
	// 删除入站之后，最后一轮照样取得到，取完才报不存在。
	(*adapters)[1].add(core.UserTraffic{ID: 2, Upload: 3, Download: 4})
	if err := c.DelInbound("fake-1"); err != nil {
		t.Fatal(err)
	}
	got, err = c.GetTraffic("fake-1")
	if up, down := sumTraffic(got); err != nil || up != 3 || down != 4 {
		t.Fatalf("删除后取到 %d/%d err=%v，期望 3/4", up, down, err)
	}
	if _, err := c.GetTraffic("fake-1"); err == nil {
		t.Fatal("退场流量取完之后应当报入站不存在")
	}
}
