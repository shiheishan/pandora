//go:build interop

// [INPUT]: 依赖 anytls.go 的 newAnyTLSAdapter，hysteria2_test.go 的 hysteriaEchoPlane，外部 github.com/anytls/sing-anytls 客户端
// [OUTPUT]: 对外提供 TestAnyTLSNativeClientTCPAndUOTUDP：第三方 AnyTLS 客户端经 NativeCore 入站完成 TCP 回显、UoT UDP 回显、流量计量与错密码拒绝
// [POS]: kernel 的 AnyTLS 互操作门，与 *_external_interop_test.go 同属 -tags interop 的非 race 选跑集
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

// 为何不在默认 race 套件里：sing-anytls v0.0.11 与 v0.0.13 的客户端内部
// 自带数据竞争（session/stream.go closeLocally 与 Write 读写同一字段），
// 栈里没有我方代码。与外部 Xray 客户端的 WaitReadCloser 竞争同理，走非
// race 的 opt-in 门；上游修复后可移回 anytls_test.go。

import (
	"context"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/util"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"
)

func TestAnyTLSNativeClientTCPAndUOTUDP(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "anytls", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	adapter, err := newAnyTLSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &hysteriaEchoPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 88, UUID: "anytls-secret"}}); err != nil {
		t.Fatal(err)
	}
	dialOut := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	}
	client, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: "anytls-secret", DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tcp, err := client.CreateProxy(ctx, M.ParseSocksaddr("echo.test:80"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tcp.Write([]byte("anytls-tcp")); err != nil {
		t.Fatal(err)
	}
	_ = tcp.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make([]byte, len("anytls-tcp"))
	if _, err := io.ReadFull(tcp, got); err != nil || string(got) != "anytls-tcp" {
		t.Fatalf("TCP echo=%q err=%v", got, err)
	}
	_ = tcp.Close()

	proxy, err := client.CreateProxy(ctx, M.Socksaddr{Fqdn: uot.MagicAddress})
	if err != nil {
		t.Fatal(err)
	}
	uotConn, err := (&uot.Client{Version: uot.Version}).DialConn(proxy, false, M.ParseSocksaddr("127.0.0.1:53"))
	if err != nil {
		t.Fatal(err)
	}
	defer uotConn.Close()
	if _, err := uotConn.WriteTo([]byte("anytls-uot"), M.ParseSocksaddr("127.0.0.1:53")); err != nil {
		t.Fatal(err)
	}
	_ = uotConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	got = make([]byte, 64)
	n, _, err := uotConn.ReadFrom(got)
	if err != nil || string(got[:n]) != "anytls-uot" {
		t.Fatalf("UoT UDP echo=%q err=%v", got[:n], err)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 || traffic[0].ID != 88 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v err=%v", traffic, err)
	}

	wrong, err := anytls.NewClient(ctx, anytls.ClientConfig{Password: "wrong-secret", DialOut: util.DialOutFunc(dialOut), Logger: logger.NOP()})
	if err != nil {
		t.Fatal(err)
	}
	wrongConn, wrongErr := wrong.CreateProxy(ctx, M.ParseSocksaddr("echo.test:80"))
	if wrongErr == nil {
		_ = wrongConn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = wrongConn.Write([]byte("force-auth"))
		probe := make([]byte, 1)
		_, wrongErr = wrongConn.Read(probe)
		_ = wrongConn.Close()
	}
	_ = wrong.Close()
	if wrongErr == nil {
		t.Fatal("wrong AnyTLS password unexpectedly authenticated")
	}
}
