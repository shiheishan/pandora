package kernel

import (
	"context"
	"net"
	"strconv"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

// 替换同一个 tag 的入站要成功，不能撞端口。
//
// 这条守的是一次真实事故：AddInbound 原本「先起新的、成功了再关旧的」，
// 但两个 socket 不能 bind 同一个地址，而我们没开 SO_REUSEPORT。结果是
// 管理员在面板改一次节点配置，节点端拉到、bind 失败、整个入站就没了，
// 直到有人手动重启服务。
//
// UDP 传输上必然触发，TCP 上同样会——只是以前没人热更新过 TCP 节点。
func TestAddInboundReplacesSameTagWithoutPortConflict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string
		port    func(*testing.T) int
	}{
		{"mkcp 走 UDP", "mkcp", reserveFreeUDPPort},
		{"tcp", "tcp", reserveTCPPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core0 := newTestNativeCore(t)
			port := tc.port(t)
			mk := func() *core.InboundConfig {
				return &core.InboundConfig{
					Tag: "replace-me", Protocol: "vless",
					Listen: "127.0.0.1", Port: port,
					Raw: map[string]any{"network": tc.network, "security": "none"},
				}
			}

			if err := core0.AddInbound(mk()); err != nil {
				t.Fatalf("首次启动失败：%v", err)
			}
			// 同一个 tag、同一个端口，改点配置再来一次——这正是面板改
			// 配置后节点端会做的事
			if err := core0.AddInbound(mk()); err != nil {
				t.Fatalf("替换失败（端口冲突就是这个 bug）：%v", err)
			}
			// 再来几次，确认不是碰巧过了一次
			for i := 0; i < 3; i++ {
				if err := core0.AddInbound(mk()); err != nil {
					t.Fatalf("第 %d 次替换失败：%v", i+3, err)
				}
			}

			// 替换完之后端口必须还在监听——不能出现「替换成功但没人在听」
			assertPortServing(t, tc.network, port)
		})
	}
}

func TestApplyInboundRejectsRoutingBeforeReplacingServingGeneration(t *testing.T) {
	core0 := newTestNativeCore(t)
	port := reserveTCPPort(t)
	cfg := &core.InboundConfig{
		Tag: "atomic-route", Protocol: "vless", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"network": "tcp", "security": "none"},
	}
	if err := core0.AddInbound(cfg); err != nil {
		t.Fatal(err)
	}
	badRouting := &core.Routing{Final: "missing-outbound"}
	if err := core0.ApplyInbound(cfg, badRouting); err == nil {
		t.Fatal("invalid routing unexpectedly replaced the serving generation")
	}
	assertPortServing(t, "tcp", port)
	if _, err := core0.GetTraffic(cfg.Tag); err != nil {
		t.Fatalf("previous generation was not retained: %v", err)
	}
}

func TestDelInboundSerializesWithSameTagReplacement(t *testing.T) {
	core0 := newTestNativeCore(t)
	port := reserveTCPPort(t)
	cfg := &core.InboundConfig{
		Tag: "delete-replace", Protocol: "vless", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"network": "tcp", "security": "none"},
	}
	if err := core0.AddInbound(cfg); err != nil {
		t.Fatal(err)
	}
	if err := core0.DelInbound(cfg.Tag); err != nil {
		t.Fatal(err)
	}
	if err := core0.AddInbound(cfg); err != nil {
		t.Fatalf("replacement after delete failed: %v", err)
	}
	assertPortServing(t, "tcp", port)
}

// assertPortServing 确认端口上确实有东西在监听。
func assertPortServing(t *testing.T, network string, port int) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if network == "mkcp" {
		// UDP 没有连接概念，用「再 bind 一次应当失败」来反证有人占着
		pc, err := net.ListenPacket("udp", addr)
		if err == nil {
			pc.Close()
			t.Fatalf("替换后 UDP %d 没人监听", port)
		}
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		t.Fatalf("替换后 TCP %d 没人监听", port)
	}
}

// reserveFreeUDPPort 借一个空闲 UDP 端口再还回去。
//
// 名字不叫 reserveUDPPort：那个住在 interop 标签的文件里，默认构建下
// 看不见，但加上标签就会和这里撞名。
func reserveFreeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return port
}

func newTestNativeCore(t *testing.T) *NativeCore {
	t.Helper()
	c := NewNativeCore(nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := c.Start(ctx); err != nil {
		t.Fatalf("内核启动失败：%v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
