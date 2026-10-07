package kernel

import (
	"context"
	"net"
	"testing"

	"github.com/aegispanel/nodeagent/core"
)

// 启动顺序：ApplyInboundWithUsers 返回时监听已开、名单已在——不需要再 AddUsers，
// 第一条连接就能认证通过（原先这段窗口里的连接全被拒成「用户未授权」）。
func TestApplyInboundWithUsersAuthorizesFromFirstAccept(t *testing.T) {
	for _, p := range lifecycleProtos() {
		t.Run(p.name, func(t *testing.T) {
			echo := startLifecycleEcho(t, false)
			reserved, _ := net.Listen("tcp", "127.0.0.1:0")
			port := reserved.Addr().(*net.TCPAddr).Port
			_ = reserved.Close()
			c := NewNativeCore(nil)
			if err := c.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			user := p.user(0)
			raw := map[string]any{}
			for k, v := range p.raw {
				raw[k] = v
			}
			cfg := &core.InboundConfig{Tag: "su-" + p.name, Protocol: p.name, Listen: "127.0.0.1", Port: port, Raw: raw}
			if err := c.ApplyInboundWithUsers(cfg, lifecycleAllowLoopback(), []core.User{user}); err != nil {
				t.Fatal(err)
			}
			conn, err := p.dial(port, user, echo.addr())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := echoOnce(conn, "first"); err != nil {
				t.Fatalf("入站一就绪，名单里的用户就应能连：%v", err)
			}
		})
	}
}

// 不支持预装的适配器（mieru）走 Start 之后立刻补装，名单同样在返回前装好。
func TestPostloadUsersForAdaptersWithoutPreStart(t *testing.T) {
	var a Adapter = &mieruAdapter{}
	if _, ok := a.(preStartUsers); ok {
		t.Fatal("mieru 的首批用户会把监听拉起来，不能声明支持预装")
	}
	for _, protocol := range []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "naive", "socks", "http", "juicity", "shadowtls"} {
		factory := NewDefaultAdapterRegistry().factories[protocol]
		adapter, err := factory(InboundSpec{Config: core.InboundConfig{Protocol: protocol, Raw: map[string]any{"method": "aes-128-gcm"}}})
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		if _, ok := adapter.(preStartUsers); !ok {
			t.Fatalf("%s 应支持 Start 之前装用户", protocol)
		}
	}
}
