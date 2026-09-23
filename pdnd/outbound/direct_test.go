package outbound

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

type stubResolver map[string][]netip.Addr

func (s stubResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	all := s[host]
	var out []netip.Addr
	for _, a := range all {
		isV4 := a.Is4() || a.Is4In6()
		switch network {
		case "ip4":
			if isV4 {
				out = append(out, a)
			}
		case "ip6":
			if !isV4 {
				out = append(out, a)
			}
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

// 出口地址一致性靠的就是这个排序。排错了不会报错，
// 只会让节点入口是 v4、出口是 v6，某些站点行为不同而我们看不出来。
func TestResolve_优先IPv4(t *testing.T) {
	r := stubResolver{"x.example": {
		netip.MustParseAddr("2001:db8::1"),
		netip.MustParseAddr("1.2.3.4"),
		netip.MustParseAddr("2001:db8::2"),
		netip.MustParseAddr("5.6.7.8"),
	}}
	d := NewDirect("direct", StrategyPreferIPv4, r)
	got, err := d.resolve(context.Background(), "x.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("应当保留全部 4 个地址（v6 作为回落），实际 %d", len(got))
	}
	if !(got[0].Is4() && got[1].Is4()) {
		t.Errorf("v4 没有排在前面: %v", got)
	}
	// 同族内部要保持解析返回的原顺序 —— 那个顺序通常已被 DNS 按就近排过
	if got[0].String() != "1.2.3.4" || got[1].String() != "5.6.7.8" {
		t.Errorf("同族内部顺序被打乱: %v", got)
	}
}

func TestResolve_优先IPv6(t *testing.T) {
	r := stubResolver{"x.example": {
		netip.MustParseAddr("1.2.3.4"),
		netip.MustParseAddr("2001:db8::1"),
	}}
	d := NewDirect("direct", StrategyPreferIPv6, r)
	got, _ := d.resolve(context.Background(), "x.example")
	if got[0].Is4() {
		t.Errorf("v6 没有排在前面: %v", got)
	}
}

// only 系是硬性限定：纯 v6 站点在 ipv4_only 下就是打不开，
// 这一点必须是明确的，不能悄悄回落。
func TestResolve_only是硬限定(t *testing.T) {
	r := stubResolver{"v6only.example": {netip.MustParseAddr("2001:db8::1")}}
	d := NewDirect("direct", StrategyIPv4Only, r)
	if _, err := d.resolve(context.Background(), "v6only.example"); err == nil {
		t.Error("ipv4_only 下纯 v6 域名应当失败，而不是回落到 v6")
	}
}

func TestRegistry(t *testing.T) {
	types := Types()
	want := map[string]bool{"direct": true, "block": true}
	for _, tp := range types {
		delete(want, tp)
	}
	if len(want) > 0 {
		t.Errorf("这些类型没注册上: %v（已注册 %v）", want, types)
	}

	if _, err := New(Options{Tag: "x", Type: "根本没有这种"}); err == nil {
		t.Error("未知类型应当报错")
	}
	if _, err := New(Options{Tag: "x", Type: ""}); err == nil {
		t.Error("空类型应当报错")
	}
	// 错误信息里要列出支持的类型，否则配错的人只能去翻源码
	_, err := New(Options{Tag: "x", Type: "nope"})
	if err == nil || !strings.Contains(err.Error(), "direct") {
		t.Errorf("错误信息应当列出已支持类型，实际: %v", err)
	}
}

func TestDirect_坏策略被拒(t *testing.T) {
	_, err := New(Options{Tag: "d", Type: "direct",
		Settings: map[string]any{"domain_strategy": "prefer_ipv7"}})
	if err == nil {
		t.Error("不认识的 domain_strategy 应当在构造时报错")
	}
}

// block 必须返回错误而不是一个立刻 EOF 的连接：调用方要能区分
// 「连上了但对方断开」和「被规则拒绝」，前者重试后者不该重试。
func TestBlock(t *testing.T) {
	b, err := New(Options{Tag: "block", Type: "block"})
	if err != nil {
		t.Fatal(err)
	}
	dest := M.ParseSocksaddrHostPort("example.com", 443)
	if _, err := b.DialTCP(context.Background(), dest); err == nil {
		t.Error("block 的 TCP 应当返回错误")
	}
	if _, err := b.ListenUDP(context.Background(), dest); err == nil {
		t.Error("block 的 UDP 应当返回错误")
	}
}

func TestSet_替换时关掉旧的(t *testing.T) {
	s := NewSet()
	a := &countingOutbound{tag: "a"}
	s.Replace(map[string]Outbound{"a": a})
	_, release, ok := s.Acquire("a")
	if !ok {
		t.Fatal("取不到刚放进去的出站")
	}
	release()

	b := &countingOutbound{tag: "a"}
	if errs := s.Replace(map[string]Outbound{"a": b}); len(errs) > 0 {
		t.Errorf("替换报错: %v", errs)
	}
	if a.closed != 1 {
		t.Errorf("旧出站没被关闭（关闭次数 %d）—— QUIC 会话会一直泄漏", a.closed)
	}
	if b.closed != 0 {
		t.Error("新出站不该被关闭")
	}
}

func TestSet_克隆输入并等待租约释放(t *testing.T) {
	s := NewSet()
	a := &countingOutbound{tag: "a", closeSignal: make(chan struct{})}
	input := map[string]Outbound{"a": a}
	s.Replace(input)
	delete(input, "a")
	if _, release, ok := s.Acquire("a"); !ok {
		t.Fatal("调用方修改输入 map 影响了集合")
	} else {
		release()
	}

	_, release, ok := s.Acquire("a")
	if !ok {
		t.Fatal("无法取得租约")
	}
	s.Replace(map[string]Outbound{"b": &countingOutbound{tag: "b"}})
	if a.closed != 0 {
		t.Fatal("仍有租约时旧出站被关闭")
	}
	release()
	select {
	case <-a.closeSignal:
	case <-time.After(time.Second):
		t.Fatal("释放租约后旧 generation 没有被关闭")
	}
	if a.closed != 1 {
		t.Fatalf("释放租约后关闭次数 = %d，want 1", a.closed)
	}
}

func TestSet_同一实例多标签只关闭一次(t *testing.T) {
	s := NewSet()
	a := &countingOutbound{tag: "shared"}
	s.Replace(map[string]Outbound{"old-a": a, "old-b": a})
	s.Replace(nil)
	if a.closed != 1 {
		t.Fatalf("同一实例多标签关闭次数 = %d，want 1", a.closed)
	}
}

// 每个 generation 独占实例，避免连续更新关闭仍被更老连接使用的会话。
func TestSet_拒绝跨Generation复用实例(t *testing.T) {
	s := NewSet()
	a := &countingOutbound{tag: "a"}
	s.Replace(map[string]Outbound{"a": a})
	errs := s.Replace(map[string]Outbound{"renamed": a})
	if len(errs) != 1 {
		t.Fatalf("跨 generation 复用错误数 = %d，want 1", len(errs))
	}
	if a.closed != 0 {
		t.Errorf("被拒绝的更新关闭了旧实例 %d 次", a.closed)
	}
	if tags := s.Tags(); !tags["a"] || tags["renamed"] {
		t.Fatalf("被拒绝的更新改变了生效配置: %v", tags)
	}
}

func TestSet_拒绝隔代复活并关闭拒绝路径的新实例(t *testing.T) {
	s := NewSet()
	a := &countingOutbound{tag: "a"}
	b := &countingOutbound{tag: "b"}
	s.Replace(map[string]Outbound{"a": a})
	s.Replace(map[string]Outbound{"b": b})
	if a.closed != 1 {
		t.Fatalf("第一代实例关闭次数 = %d，want 1", a.closed)
	}

	newButRejected := &countingOutbound{tag: "new"}
	errs := s.Replace(map[string]Outbound{"resurrected": a, "new": newButRejected})
	if len(errs) == 0 {
		t.Fatal("已关闭的历史实例被重新安装")
	}
	if newButRejected.closed != 1 {
		t.Fatalf("拒绝路径的新实例关闭次数 = %d，want 1", newButRejected.closed)
	}
	if errs := s.Replace(map[string]Outbound{"closed-fresh": newButRejected}); len(errs) == 0 {
		t.Fatal("拒绝路径中已关闭的新实例被重新安装")
	}
	if tags := s.Tags(); !tags["b"] || tags["resurrected"] || tags["new"] || tags["closed-fresh"] {
		t.Fatalf("被拒绝的隔代复用改变了生效配置: %v", tags)
	}
}

type countingOutbound struct {
	instanceIdentity
	tag         string
	closed      int
	closeSignal chan struct{}
	closeOnce   sync.Once
}

func (c *countingOutbound) Tag() string  { return c.tag }
func (c *countingOutbound) Type() string { return "stub" }
func (c *countingOutbound) Close() error {
	c.closed++
	if c.closeSignal != nil {
		c.closeOnce.Do(func() { close(c.closeSignal) })
	}
	return nil
}
func (c *countingOutbound) DialTCP(context.Context, M.Socksaddr) (net.Conn, error) {
	return nil, nil
}
func (c *countingOutbound) ListenUDP(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, nil
}
