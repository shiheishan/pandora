package kernel

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

// fakeRealityDest 模拟一个 dest：读完客户端转来的 ClientHello，回一段指定记录长度
// 的 TLS 1.3 服务端飞行（ServerHello、CCS、若干条 application_data），然后像真站点
// 一样等客户端的 Finished、不再发东西。REALITY 只看这些记录的类型与长度。
func fakeRealityDest(t *testing.T, appDataRecords []int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				var head [5]byte
				if _, err := io.ReadFull(conn, head[:]); err != nil {
					return
				}
				hello := make([]byte, binary.BigEndian.Uint16(head[3:5]))
				if _, err := io.ReadFull(conn, hello); err != nil || len(hello) < 39 {
					return
				}
				// 握手头 4 + legacy_version 2 + random 32，之后是 legacy_session_id：真站点原样回显。
				sessionID := hello[39 : 39+int(hello[38])]
				flight := append(fakeServerHelloRecord(sessionID), 0x14, 0x03, 0x03, 0x00, 0x01, 0x01)
				for _, n := range appDataRecords {
					body := make([]byte, n-5)
					_, _ = rand.Read(body)
					flight = append(flight, 0x17, 0x03, 0x03)
					flight = binary.BigEndian.AppendUint16(flight, uint16(len(body)))
					flight = append(flight, body...)
				}
				_, _ = conn.Write(flight)
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

// fakeServerHelloRecord 是一条合法的 TLS 1.3 ServerHello（X25519、TLS_AES_128_GCM_SHA256）。
func fakeServerHelloRecord(session []byte) []byte {
	var body []byte
	body = append(body, 0x03, 0x03)
	random := make([]byte, 32)
	_, _ = rand.Read(random)
	body = append(body, random...)
	body = append(body, byte(len(session)))
	body = append(body, session...)
	body = append(body, 0x13, 0x01, 0x00)
	share := make([]byte, 32)
	_, _ = rand.Read(share)
	var ext []byte
	ext = append(ext, 0x00, 0x2b, 0x00, 0x02, 0x03, 0x04)
	ext = append(ext, 0x00, 0x33, 0x00, 0x24, 0x00, 0x1d, 0x00, 0x20)
	ext = append(ext, share...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(ext)))
	body = append(body, ext...)
	msg := []byte{0x02, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	msg = append(msg, body...)
	record := []byte{0x16, 0x03, 0x03}
	record = binary.BigEndian.AppendUint16(record, uint16(len(msg)))
	return append(record, msg...)
}

// TestRealityDestFlightShapes：10-08 真节点测试里 dest 选 www.microsoft.com 报
// 「target TLS record length invalid」（证书记录 8273 字节，超过 8192 上限）；
// www.cloudflare.com、dl.google.com 握手挂约 15 秒后 EOF（它们把 EncryptedExtensions
// 到 Finished 并成一条记录，握手 worker 等不到「后面几条」）。记录长度取自 2026-10
// 实测这几个站点对 Chrome 指纹的响应。
func TestRealityDestFlightShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		records []int
	}{
		{"分条且证书小（apple 形状，原本就通）", []int{41, 3375, 286, 58}},
		{"证书记录超过 8192（microsoft 形状）", []int{41, 8273, 286, 74}},
		{"握手消息并成一条（cloudflare 形状）", []int{2049}},
		{"握手消息并成一条且较大（dl.google.com 形状）", []int{5148}},
		{"单条接近 TLS 记录上限", []int{41, 16401, 286, 74}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := fakeRealityDest(t, tc.records)
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			shortID := [8]byte{4, 2, 4, 2, 4, 2, 4, 2}
			listener, err := ListenReality("tcp", "127.0.0.1:0", RealityServerConfig{
				Dest:        dest,
				ServerNames: map[string]bool{"example.com": true},
				PrivateKey:  key.Bytes(),
				ShortIDs:    map[[8]byte]bool{shortID: true},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr == nil {
					accepted <- conn
				}
			}()
			raw, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			// 修复前 cloudflare 形状会挂到握手 worker 的 15 秒截止：这里给 5 秒。
			_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := xrayreality.UClient(raw, &xrayreality.Config{Fingerprint: "chrome", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: shortID[:]}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
			if err != nil {
				t.Fatalf("REALITY 握手: %v", err)
			}
			defer client.Close()
			select {
			case server := <-accepted:
				defer server.Close()
				if _, err := client.Write([]byte("ping")); err != nil {
					t.Fatal(err)
				}
				got := make([]byte, 4)
				_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
				if _, err := io.ReadFull(server, got); err != nil || string(got) != "ping" {
					t.Fatalf("握手后读 = %q, %v", got, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("服务端没有交出已认证的连接")
			}
		})
	}
}
