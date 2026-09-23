//go:build interop

package kernel

// 真 xray 客户端跑 VLESS over mKCP，穿过我们的节点访问上游。
//
// mkcp 包内部已经和 xray 的 kcp 双向互通过，但那验证的是传输层本身。
// 这里验证的是接进内核之后整条链路还成立：客户端的 socks 入站 → VLESS
// 编码 → mKCP → 我们的监听器 → VLESS 解码 → 出站到上游，再原路回来。
// 中间任何一层对 net.Conn 语义的假设不成立（比如指望它是 TCP），
// 都会在这里暴露。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xraycore "github.com/xtls/xray-core/core"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// 不加掩码的裸 mKCP。
func TestExternalXrayVLESSMKCPInterop(t *testing.T) {
	runXrayMKCPInterop(t, "", "")
}

// 加了 aes128gcm 掩码的 mKCP。
//
// 这一条比上面那条更接近真实部署：裸 mKCP 的包在网上有明显特征，实际
// 上线基本都会套一层。它也是掩码层唯一的端到端验证——掩码自己的单测
// 只证明了「我们和 xray 的解密逻辑对得上」，这里证明的是整条链路：
// xray 客户端加密 → 网络 → 我们解密 → mKCP → VLESS → 出站，再原路回来。
func TestExternalXrayVLESSMKCPMaskedInterop(t *testing.T) {
	runXrayMKCPInterop(t, "mkcp-aes128gcm", "pandora-mask-e2e")
}

func runXrayMKCPInterop(t *testing.T, maskType, maskPassword string) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()

	serverPort := reserveUDPPort(t)
	clientPort := reserveTCPPort(t)
	u := uuid.New()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-xray-mkcp", Protocol: "vless",
		Listen: "127.0.0.1", Port: serverPort,
		Raw: mkcpInteropRaw(maskType, maskPassword),
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.AddUsers([]core.User{{ID: 7101, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	// 客户端一侧全部用 xray 的默认 mKCP 参数——真实用户不会去调这些，
	// 要能对上的是默认值。
	config := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen": "127.0.0.1", "port": clientPort, "protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": "127.0.0.1", "port": serverPort,
					"users": []any{map[string]any{"id": u.String(), "encryption": "none"}},
				}},
			},
			"streamSettings": xrayMKCPStreamSettings(maskType, maskPassword),
		}},
	}
	configBytes, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	xray, err := xraycore.StartInstance("json", configBytes)
	if err != nil {
		t.Fatalf("启动 xray 客户端失败：%v", err)
	}
	defer xray.Close()

	conn := dialSocksWithRetry(t, clientPort, upstream.Addr().(*net.TCPAddr))
	defer conn.Close()

	// 一小一大两轮。小的验证链路通，大的把 mKCP 的分段、窗口和重组
	// 都走一遍——上层 VLESS 只要有一个字节错位，回声就对不上。
	for _, payload := range [][]byte{
		[]byte("vless over mkcp"),
		bytes.Repeat([]byte("pandora-mkcp-"), 8192), // ~104 KB
	} {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		writeErr := make(chan error, 1)
		go func(p []byte) { _, err := conn.Write(p); writeErr <- err }(payload)

		got := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("读回声失败（%d 字节负载）：%v", len(payload), err)
		}
		if err := <-writeErr; err != nil {
			t.Fatalf("写入失败：%v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("%d 字节负载的回声内容不一致", len(payload))
		}
	}
}

// dialSocksWithRetry 连上 xray 的 socks 入站并完成握手。
//
// 要重试：xray 实例启动是异步的，StartInstance 返回时 socks 端口
// 未必已经在监听。
func dialSocksWithRetry(t *testing.T, port int, dest *net.TCPAddr) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("连接 xray socks 入站失败：%v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(methodReply, []byte{5, 0}) {
		t.Fatalf("socks 协商回复 = %v", methodReply)
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, dest.IP.To4()...)
	request = append(request, byte(dest.Port>>8), byte(dest.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		t.Fatalf("socks CONNECT 被拒，状态码 %d", reply[1])
	}
	return conn
}

// mkcpInteropRaw 组一份节点侧配置。
func mkcpInteropRaw(maskType, maskPassword string) map[string]any {
	raw := map[string]any{"network": "mkcp", "security": "none"}
	if maskType != "" {
		raw["mask"] = maskType
		raw["mask_password"] = maskPassword
	}
	return raw
}

// xrayMKCPStreamSettings 组一份 xray 客户端侧的流设置。
//
// 掩码用上游的 finalmask.udp 格式——这里刻意不走我们自己的扁平写法，
// 因为要验证的正是「照着 xray 文档配的客户端能不能连上我们」。
func xrayMKCPStreamSettings(maskType, maskPassword string) map[string]any {
	settings := map[string]any{
		"network":     "mkcp",
		"security":    "none",
		"kcpSettings": map[string]any{},
	}
	if maskType != "" {
		settings["finalmask"] = map[string]any{
			"udp": []any{map[string]any{
				"type":     maskType,
				"settings": map[string]any{"password": maskPassword},
			}},
		}
	}
	return settings
}
