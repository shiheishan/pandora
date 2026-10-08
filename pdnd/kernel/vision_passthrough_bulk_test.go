package kernel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

// REALITY + Vision 承载非 TLS 流量：前几个包填充、随后 command=1 结束填充，服务端
// 读侧进入透传并打开 REALITY 的机会式多读、写侧多条记录合并写。双向各 2MB 原样
// 回显，字节一个不差（守卫 internal/reality 的 SetReadCoalescing 与合并写）。
func TestVLESSRealityVisionPlainPassthroughBulk(t *testing.T) {
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoLn.Close()
	go func() {
		for {
			conn, acceptErr := echoLn.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	go serveTestTLSDecoy(t, decoyRaw)

	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{7, 7, 0, 1, 2, 3, 4, 6}
	port := reserveTCPPort(t)
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"security": "reality", "flow": FlowVision,
		"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
		"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{"0707000102030406"},
	}}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7502, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(echoLn.Addr().(*net.TCPAddr)).Unwrap()},
	}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))
	outer, err := xrayreality.UClient(raw, &xrayreality.Config{Fingerprint: "chrome", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: shortID[:]}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(port)))
	if err != nil {
		t.Fatalf("xray REALITY 握手: %v", err)
	}
	target := echoLn.Addr().(*net.TCPAddr)
	addons := EncodeVLESSAddons(VLESSAddons{Flow: FlowVision})
	header := append([]byte{vlessVersion}, id[:]...)
	header = append(header, byte(len(addons)))
	header = append(header, addons...)
	header = append(header, vlessTCP)
	header = binary.BigEndian.AppendUint16(header, uint16(target.Port))
	header = append(header, 1)
	header = append(header, target.IP.To4()...)
	if _, err := outer.Write(header); err != nil {
		t.Fatal(err)
	}
	client := &visionDirectTestClient{outer: outer, raw: raw, state: newVisionState([][]byte{id[:]}).asClient(id[:])}
	respHead := make([]byte, 2)
	if _, err := io.ReadFull(outer, respHead); err != nil || respHead[0] != vlessVersion {
		t.Fatalf("VLESS 响应头 %v: %v", respHead, err)
	}

	// 先来回十几轮小消息，把双方的填充与 TLS 识别都走完（packetsToFilter=8）。
	for i := 0; i < 12; i++ {
		ping := bytes.Repeat([]byte{byte('a' + i)}, 300)
		if _, err := client.Write(ping); err != nil {
			t.Fatalf("第 %d 轮写: %v", i, err)
		}
		pong := make([]byte, len(ping))
		if _, err := io.ReadFull(client, pong); err != nil || !bytes.Equal(pong, ping) {
			t.Fatalf("第 %d 轮回显: %v", i, err)
		}
	}
	client.state.mu.Lock()
	padding := client.state.isPadding
	client.state.mu.Unlock()
	if padding {
		t.Fatal("小消息走完了客户端仍在填充，测不到透传")
	}

	payload := make([]byte, 2<<20)
	_, _ = rand.Read(payload)
	writeErr := make(chan error, 1)
	go func() {
		for off := 0; off < len(payload); off += 48 << 10 {
			end := min(off+48<<10, len(payload))
			if _, err := client.Write(payload[off:end]); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatalf("回显: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("透传后的回显内容不符")
	}
}

// serveTestTLSDecoy 是 REALITY dest：任意 TLS 1.3 站点，读到断开为止。
func serveTestTLSDecoy(t *testing.T, ln net.Listener) {
	decoy := tls.NewListener(ln, testXHTTPServerTLSConfig(t))
	for {
		conn, err := decoy.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
}
