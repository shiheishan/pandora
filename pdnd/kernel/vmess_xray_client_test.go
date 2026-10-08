package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	xvmess "github.com/xtls/xray-core/proxy/vmess"
	"github.com/xtls/xray-core/proxy/vmess/encoding"
)

// xrayVMessRequest 按 Xray 出站（proxy/vmess/outbound/outbound.go Process）的
// 规则组装请求头选项：aes-128-gcm / chacha20-poly1305 / none 开 ChunkMasking，
// aes / chacha / auto 再开 GlobalPadding，zero 降为 none 并清掉分块。
// 这里只用 Xray 的编码库（同步、无后台 goroutine），所以能进默认的 -race 套件；
// 带完整 Xray 实例的端到端在 -tags interop 的 TestExternalXrayVMessInterop。
func xrayVMessRequest(t *testing.T, id uuid.UUID, security protocol.SecurityType, command protocol.RequestCommand, port int) *protocol.RequestHeader {
	t.Helper()
	account, err := (&xvmess.Account{Id: id.String(), SecuritySettings: &protocol.SecurityConfig{Type: security}}).AsAccount()
	if err != nil {
		t.Fatal(err)
	}
	request := &protocol.RequestHeader{
		Version: encoding.Version,
		User:    &protocol.MemoryUser{Account: account},
		Command: command,
		Address: xnet.ParseAddress("127.0.0.1"),
		Port:    xnet.Port(port),
		Option:  protocol.RequestOptionChunkStream,
	}
	request.Security = account.(*xvmess.MemoryAccount).Security
	if request.Security == protocol.SecurityType_AES128_GCM || request.Security == protocol.SecurityType_NONE || request.Security == protocol.SecurityType_CHACHA20_POLY1305 {
		request.Option.Set(protocol.RequestOptionChunkMasking)
	}
	if (security == protocol.SecurityType_AES128_GCM || security == protocol.SecurityType_CHACHA20_POLY1305 || security == protocol.SecurityType_AUTO) && request.Option.Has(protocol.RequestOptionChunkMasking) {
		request.Option.Set(protocol.RequestOptionGlobalPadding)
	}
	if request.Security == protocol.SecurityType_ZERO {
		request.Security = protocol.SecurityType_NONE
		request.Option.Clear(protocol.RequestOptionChunkStream)
		request.Option.Clear(protocol.RequestOptionChunkMasking)
	}
	return request
}

// TestVMessXrayClientEncoding：10-08 真节点测试里 Xray 内核客户端（v2rayN /
// v2rayNG 等）连 VMess 节点全部失败——响应头首字节没回显请求头的 V 字节，且没实现
// Xray 对 AEAD 默认开启的 GlobalPadding。订阅给的是 scy=auto。
func TestVMessXrayClientEncoding(t *testing.T) {
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
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	upstreamPort := upstream.Addr().(*net.TCPAddr).Port
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	adapter := &vmessAdapter{users: make(map[string]vmessUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7401, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	for _, tc := range []struct {
		name     string
		security protocol.SecurityType
	}{
		{"auto", protocol.SecurityType_AUTO},
		{"aes-128-gcm", protocol.SecurityType_AES128_GCM},
		{"chacha20-poly1305", protocol.SecurityType_CHACHA20_POLY1305},
		{"none", protocol.SecurityType_NONE},
		{"zero", protocol.SecurityType_ZERO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			request := xrayVMessRequest(t, id, tc.security, protocol.RequestCommandTCP, upstreamPort)
			session := encoding.NewClientSession(context.Background(), 0)
			if err := session.EncodeRequestHeader(request, conn); err != nil {
				t.Fatal(err)
			}
			bodyWriter, err := session.EncodeRequestBody(request, conn)
			if err != nil {
				t.Fatal(err)
			}
			// 跨多个分块（Xray 每块约 8KB），覆盖填充与掩码的序列推进。
			payload := make([]byte, 40<<10)
			_, _ = rand.Read(payload)
			if err := bodyWriter.WriteMultiBuffer(buf.MergeBytes(nil, payload)); err != nil {
				t.Fatal(err)
			}
			reader := &buf.BufferedReader{Reader: buf.NewReader(conn)}
			if _, err := session.DecodeResponseHeader(reader); err != nil {
				t.Fatalf("Xray 解响应头: %v", err)
			}
			bodyReader, err := session.DecodeResponseBody(request, reader)
			if err != nil {
				t.Fatal(err)
			}
			var got []byte
			for len(got) < len(payload) {
				mb, err := bodyReader.ReadMultiBuffer()
				if err != nil {
					t.Fatalf("Xray 读响应体（已读 %d/%d）: %v", len(got), len(payload), err)
				}
				for _, b := range mb {
					got = append(got, b.Bytes()...)
				}
				buf.ReleaseMulti(mb)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("回显不符：len=%d", len(got))
			}
		})
	}
}

// TestVMessXrayClientUDP：UDP 走 Xray 的按包分块（TransferTypePacket），auto 时每个
// 包都带 GlobalPadding；none 时是不带认证标签的分块，同样可带掩码与填充。
func TestVMessXrayClientUDP(t *testing.T) {
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		packet := make([]byte, 2048)
		for {
			n, addr, readErr := upstream.ReadFromUDP(packet)
			if readErr != nil {
				return
			}
			_, _ = upstream.WriteToUDP(packet[:n], addr)
		}
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	id := uuid.New()
	adapter := &vmessAdapter{users: make(map[string]vmessUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	if err := adapter.AddUsers([]core.User{{ID: 7402, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vmessUDPTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	for _, tc := range []struct {
		name     string
		security protocol.SecurityType
		padding  bool
	}{
		{"auto", protocol.SecurityType_AUTO, false},
		{"none", protocol.SecurityType_NONE, false},
		// Xray 设了 XRAY_VMESS_PADDING 时 none 也开 GlobalPadding：UDP 按包加填充。
		{"none+padding", protocol.SecurityType_NONE, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			request := xrayVMessRequest(t, id, tc.security, protocol.RequestCommandUDP, upstream.LocalAddr().(*net.UDPAddr).Port)
			if tc.padding {
				request.Option.Set(protocol.RequestOptionGlobalPadding)
			}
			session := encoding.NewClientSession(context.Background(), 0)
			if err := session.EncodeRequestHeader(request, conn); err != nil {
				t.Fatal(err)
			}
			bodyWriter, err := session.EncodeRequestBody(request, conn)
			if err != nil {
				t.Fatal(err)
			}
			reader := &buf.BufferedReader{Reader: buf.NewReader(conn)}
			var bodyReader buf.Reader
			for i := 0; i < 3; i++ {
				packet := []byte("xray-vmess-udp-" + tc.name + "-" + itoa(i))
				if err := bodyWriter.WriteMultiBuffer(buf.MergeBytes(nil, packet)); err != nil {
					t.Fatal(err)
				}
				if bodyReader == nil {
					if _, err := session.DecodeResponseHeader(reader); err != nil {
						t.Fatalf("Xray 解响应头: %v", err)
					}
					if bodyReader, err = session.DecodeResponseBody(request, reader); err != nil {
						t.Fatal(err)
					}
				}
				mb, err := bodyReader.ReadMultiBuffer()
				if err != nil {
					t.Fatalf("Xray 读第 %d 个回包: %v", i, err)
				}
				var got []byte
				for _, b := range mb {
					got = append(got, b.Bytes()...)
				}
				buf.ReleaseMulti(mb)
				if !bytes.Equal(got, packet) {
					t.Fatalf("第 %d 个回包 = %q, want %q", i, got, packet)
				}
			}
		})
	}
}

// TestVMessAuthenticatedLengthRejectedPromptly（审查 VMess 4）：开了 AuthenticatedLength
// 的客户端已经通过认证，读完请求头就该拒绝并断开，而不是按探测处理、读满 10 秒。
func TestVMessAuthenticatedLengthRejectedPromptly(t *testing.T) {
	port := reserveTCPPort(t)
	id := uuid.New()
	adapter := &vmessAdapter{users: make(map[string]vmessUser), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	if err := adapter.AddUsers([]core.User{{ID: 7405, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	request := xrayVMessRequest(t, id, protocol.SecurityType_AES128_GCM, protocol.RequestCommandTCP, 443)
	request.Option.Set(protocol.RequestOptionAuthenticatedLength)
	session := encoding.NewClientSession(context.Background(), 0)
	if err := session.EncodeRequestHeader(request, conn); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("连接没有被干净关闭：%v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("AuthenticatedLength 请求 %v 后才被断开", took)
	}
}
