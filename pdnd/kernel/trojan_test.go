package kernel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

func TestTrojanPasswordProofAndRequest(t *testing.T) {
	if len(trojanPasswordProof("secret")) != 56 {
		t.Fatalf("proof length = %d", len(trojanPasswordProof("secret")))
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.Write([]byte(trojanPasswordProof("secret")))
		_, _ = client.Write([]byte("\r\n"))
		// CMD | ATYP | 域名长度 | 域名 | 端口 | CRLF。
		// Trojan 请求头没有 SOCKS5 的 VER 和 RSV——这个测试原先按
		// {5,1,0,3,...} 编码，和当时同样搞错的解析端恰好自洽，真实
		// 客户端一连就露馅。
		request := []byte{1, 3, 11}
		request = append(request, []byte("example.com")...)
		request = append(request, 0x01, 0xbb, '\r', '\n')
		_, _ = client.Write(request)
	}()
	user, destination, err := readTrojanRequest(server, func(proof string) (core.User, bool) {
		return core.User{ID: 9, UUID: "secret"}, trojanProofEqual(proof, trojanPasswordProof("secret"))
	})
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 9 || destination.Domain != "example.com" || destination.Port != 443 {
		t.Fatalf("user=%+v destination=%+v", user, destination)
	}
}

func TestTrojanAdapterLoopbackTCPAndTraffic(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()

	adapter := &trojanAdapter{users: make(map[string]trojanUser), traffic: make(map[int64]core.UserTraffic), online: make(map[int64]map[string]struct{}), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port}}
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	plane := &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	if err := adapter.AddUsers([]core.User{{ID: 42, UUID: "secret"}}); err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	header := []byte(trojanPasswordProof("secret") + "\r\n")
	// CMD | ATYP | DST.ADDR | DST.PORT | CRLF。地址在端口前面——
	// 这里原先反过来写，解析出的目标是个垃圾地址，而测试用的
	// DataPlane 目标写死、根本不看传进来的地址，所以一直没露。
	header = append(header, 1, 1)
	header = append(header, 127, 0, 0, 1)
	var targetPort [2]byte
	binary.BigEndian.PutUint16(targetPort[:], 443)
	header = append(header, targetPort[:]...)
	header = append(header, '\r', '\n')
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("native-trojan")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("native-trojan"))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "native-trojan" {
		t.Fatalf("echo=%q", got)
	}
	// 请求头写的是 127.0.0.1:443。按 SOCKS5 格式错读会得到 1.187.127.0:1 之类的垃圾。
	assertDialedTarget(t, plane, "127.0.0.1:443")
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 42 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}

func TestTrojanAdapterRealityXrayInterop(t *testing.T) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	targetRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetRaw.Close()
	targetTLS := testXHTTPServerTLSConfig(t)
	targetTLS.NextProtos = []string{"h2", "http/1.1"}
	target := tls.NewListener(targetRaw, targetTLS)
	defer target.Close()
	go func() {
		for {
			conn, acceptErr := target.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	_ = reserved.Close()
	password := "native-trojan-reality"
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "trojan", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"security":     "reality",
		"dest":         targetRaw.Addr().String(),
		"server_names": []string{"example.com"},
		"private_key":  base64.RawURLEncoding.EncodeToString(key.Bytes()),
		"short_ids":    []string{"0102030405060708"},
	}}}
	adapterValue, err := newTrojanAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*trojanAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 904, UUID: password}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	raw, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)))
	if err != nil {
		t.Fatal(err)
	}
	client, err := xrayreality.UClient(raw, &xrayreality.Config{Fingerprint: "firefox", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: append([]byte(nil), shortID[:]...)}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(443)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	header := []byte(trojanPasswordProof(password) + "\r\n")
	// CMD | ATYP | DST.ADDR | DST.PORT | CRLF。地址在端口前面——
	// 这里原先反过来写，解析出的目标是个垃圾地址，而测试用的
	// DataPlane 目标写死、根本不看传进来的地址，所以一直没露。
	header = append(header, 1, 1)
	header = append(header, 127, 0, 0, 1)
	var targetPort [2]byte
	binary.BigEndian.PutUint16(targetPort[:], 443)
	header = append(header, targetPort[:]...)
	header = append(header, '\r', '\n')
	payload := []byte("native-trojan-reality")
	if _, err := client.Write(append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("reality trojan response=%q", got)
	}
	_ = client.Close()
	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reality trojan upstream did not finish")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		adapter.mu.RLock()
		observed := adapter.traffic[904]
		adapter.mu.RUnlock()
		if observed.Upload > 0 && observed.Download > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reality trojan traffic was not fully accounted: %+v", observed)
		}
		time.Sleep(5 * time.Millisecond)
	}
	traffic, err := adapter.SnapshotTraffic()
	if err != nil {
		t.Fatal(err)
	}
	if len(traffic) != 1 || traffic[0].ID != 904 || traffic[0].Upload == 0 || traffic[0].Download == 0 {
		t.Fatalf("traffic=%+v", traffic)
	}
}
