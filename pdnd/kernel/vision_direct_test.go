package kernel

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	utls "github.com/refraction-networking/utls"
	M "github.com/sagernet/sing/common/metadata"
	xnet "github.com/xtls/xray-core/common/net"
	xrayreality "github.com/xtls/xray-core/transport/internet/reality"
)

// visionDirectTestClient 是测试用的 Vision 客户端：外层用 Xray 的 REALITY 客户端，
// 直通的切换照 Xray（proxy.go 的 VisionWriter / VisionReader）独立实现——
// 发出 command=2 的帧之后写裸 TCP；收到 command=2 之后先取走外层 TLS 已缓冲的
// input / rawInput（Xray 用反射读同名字段，这里一样），再读裸 TCP。
// 帧格式借 visionState（填充编码早已被三个真实客户端验证过，这里要测的只是直通）。
// visionHoldConn 在合包模式下把客户端发出 command=2 那一帧之后的写攒住，与下一段
// 裸字节合成一次 TCP 写：服务端一次读到「外层记录 + 裸字节」，裸字节落进外层的
// rawInput，直通切换必须把它交出来（审查 V2）。
type visionHoldConn struct {
	net.Conn
	mu   sync.Mutex
	hold bool
	buf  []byte
}

func (h *visionHoldConn) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hold {
		h.buf = append(h.buf, p...)
		return len(p), nil
	}
	return h.Conn.Write(p)
}

func (h *visionHoldConn) setHold(v bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hold = v
	if !v && len(h.buf) > 0 {
		_, err := h.Conn.Write(h.buf)
		h.buf = nil
		return err
	}
	return nil
}

type visionDirectTestClient struct {
	outer net.Conn // *xrayreality.UConn 或 *utls.UConn
	raw   net.Conn
	hold  *visionHoldConn // 合包模式才有
	state *visionState

	readMu     sync.Mutex
	pending    []byte
	readDirect io.Reader

	writeMu     sync.Mutex
	writeDirect bool
	sawCommand2 bool
}

func (c *visionDirectTestClient) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.pending) == 0 {
		if c.readDirect != nil {
			return c.readDirect.Read(p)
		}
		buf := make([]byte, 32<<10)
		n, err := c.outer.Read(buf)
		if n > 0 {
			c.state.mu.Lock()
			chunk := buf[:n]
			if !c.state.readerDirect && (c.state.withinPaddingBuffers || c.state.packetsToFilter > 0) {
				chunk = c.state.unpad(chunk)
			}
			if c.state.packetsToFilter > 0 && len(chunk) > 0 {
				c.state.filterTLS(chunk)
			}
			direct := c.state.readerDirect
			c.state.mu.Unlock()
			c.pending = append(c.pending, chunk...)
			if direct {
				leftover := takeUTLSBuffered(c.outer)
				c.readDirect = io.MultiReader(bytes.NewReader(leftover), c.raw)
			}
		}
		if err != nil && len(c.pending) == 0 {
			return 0, err
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *visionDirectTestClient) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeDirect {
		n, err := c.raw.Write(p)
		if c.hold != nil {
			if flushErr := c.hold.setHold(false); flushErr != nil {
				return 0, flushErr
			}
		}
		return n, err
	}
	c.state.mu.Lock()
	if c.state.packetsToFilter > 0 {
		c.state.filterTLS(p)
	}
	if !c.state.isPadding {
		c.state.mu.Unlock()
		return c.outer.Write(p)
	}
	frames := c.state.buildPaddedFrames(p)
	direct := c.state.writerDirect
	c.state.mu.Unlock()
	if direct && c.hold != nil {
		// 下一次直通写会把攒住的一起冲出去；客户端接下来若只读不写，到点也冲出去。
		_ = c.hold.setHold(true)
		hold := c.hold
		time.AfterFunc(200*time.Millisecond, func() { _ = hold.setHold(false) })
	}
	for _, frame := range frames {
		if _, err := c.outer.Write(frame); err != nil {
			return 0, err
		}
	}
	if direct {
		c.writeDirect, c.sawCommand2 = true, true
	}
	return len(p), nil
}

func (c *visionDirectTestClient) Close() error                      { _ = c.raw.Close(); return c.outer.Close() }
func (c *visionDirectTestClient) LocalAddr() net.Addr               { return c.raw.LocalAddr() }
func (c *visionDirectTestClient) RemoteAddr() net.Addr              { return c.raw.RemoteAddr() }
func (c *visionDirectTestClient) SetDeadline(t time.Time) error     { return c.raw.SetDeadline(t) }
func (c *visionDirectTestClient) SetReadDeadline(t time.Time) error { return c.raw.SetReadDeadline(t) }
func (c *visionDirectTestClient) SetWriteDeadline(t time.Time) error {
	return c.raw.SetWriteDeadline(t)
}

// takeUTLSBuffered 读出 utls.Conn 已缓冲的 input 与 rawInput（照 Xray outbound 的反射写法）。
func takeUTLSBuffered(outer net.Conn) []byte {
	var conn *utls.Conn
	switch c := outer.(type) {
	case *xrayreality.UConn:
		conn = c.UConn.Conn
	case *utls.UConn:
		conn = c.Conn
	default:
		panic("unexpected outer conn")
	}
	v := reflect.ValueOf(conn).Elem()
	input := (*bytes.Reader)(unsafe.Pointer(v.FieldByName("input").UnsafeAddr()))
	rawInput := (*bytes.Buffer)(unsafe.Pointer(v.FieldByName("rawInput").UnsafeAddr()))
	var out []byte
	if input.Len() > 0 {
		plain := make([]byte, input.Len())
		_, _ = input.Read(plain)
		out = append(out, plain...)
	}
	out = append(out, rawInput.Bytes()...)
	rawInput.Reset()
	return out
}

// TestVLESSRealityVisionInnerTLS13Direct：10-08 真节点测试里 VLESS+REALITY+Vision
// 对内层 TLS 1.3 的流量三个客户端都报 bad record mac——服务端发了 command=2
// （直通）却仍经 REALITY 外层加密读写。这里让客户端经代理做一次真 TLS 1.3 握手并
// 双向传数据，再核对直通后的流量统计与踢人。外层是普通 TLS 时同理（tls 子测试）。
func TestVLESSRealityVisionInnerTLS13Direct(t *testing.T) {
	for _, outer := range []string{"reality", "tls"} {
		t.Run(outer, func(t *testing.T) { runVisionInnerTLS13Direct(t, outer, false) })
		// 合包：command=2 那一帧与之后的裸字节同一次 TCP 写到达（审查 V2）。
		t.Run(outer+"+合包", func(t *testing.T) { runVisionInnerTLS13Direct(t, outer, true) })
	}
}

func runVisionInnerTLS13Direct(t *testing.T, outerKind string, coalesce bool) {
	// 目标站：真 TLS 1.3 回显服务。
	targetTLS := testXHTTPServerTLSConfig(t)
	targetTLS.MinVersion = tls.VersionTLS13
	targetLn, err := tls.Listen("tcp", "127.0.0.1:0", targetTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer targetLn.Close()
	go func() {
		for {
			conn, acceptErr := targetLn.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	// REALITY dest：任意 TLS 1.3 站点。
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	go func() {
		for {
			conn, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	shortID := [8]byte{7, 7, 0, 1, 2, 3, 4, 5}
	port := reserveTCPPort(t)
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vless", Listen: "127.0.0.1", Port: port, Raw: map[string]any{
		"security": "reality", "flow": FlowVision,
		"dest": decoyRaw.Addr().String(), "server_names": []any{"example.com"},
		"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()), "short_ids": []any{"0707000102030405"},
	}}}
	if outerKind == "tls" {
		certPath, keyPath := testXHTTPServerCertFiles(t)
		spec.Config.Raw = map[string]any{"flow": FlowVision, "tls": true, "cert_path": certPath, "key_path": keyPath}
	}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7501, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var serverErrMu sync.Mutex
	var serverErrs []string
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(targetLn.Addr().(*net.TCPAddr)).Unwrap()},
		OnConnError: func(ce ConnError) {
			serverErrMu.Lock()
			serverErrs = append(serverErrs, ce.Stage+": "+ce.Err.Error())
			serverErrMu.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	serverErrors := func() []string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		return append([]string(nil), serverErrs...)
	}

	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	var hold *visionHoldConn
	clientRaw := raw
	if coalesce {
		hold = &visionHoldConn{Conn: raw}
		clientRaw = hold
	}
	var outer net.Conn
	if outerKind == "tls" {
		uc := utls.UClient(clientRaw, &utls.Config{InsecureSkipVerify: true, ServerName: "localhost"}, utls.HelloChrome_Auto) //nolint:gosec // 测试自签证书
		if err := uc.Handshake(); err != nil {
			t.Fatalf("外层 TLS 握手: %v", err)
		}
		if uc.ConnectionState().Version != tls.VersionTLS13 {
			t.Fatalf("外层 TLS 版本 %x，Vision 要求 1.3", uc.ConnectionState().Version)
		}
		outer = uc
	} else {
		outer, err = xrayreality.UClient(clientRaw, &xrayreality.Config{Fingerprint: "chrome", ServerName: "example.com", PublicKey: key.PublicKey().Bytes(), ShortId: shortID[:]}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(port)))
		if err != nil {
			t.Fatalf("xray REALITY 握手: %v", err)
		}
	}
	// VLESS 请求头（裸，flow=xtls-rprx-vision），目标是 TLS 1.3 回显站。
	target := targetLn.Addr().(*net.TCPAddr)
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
	client := &visionDirectTestClient{outer: outer, raw: clientRaw, hold: hold, state: newVisionState([][]byte{id[:]}).asClient(id[:])}
	// VLESS 响应头两个字节在 Vision 之外，先读掉。
	respHead := make([]byte, 2)
	if _, err := io.ReadFull(outer, respHead); err != nil || respHead[0] != vlessVersion {
		t.Fatalf("VLESS 响应头 %v: %v（服务端：%v）", respHead, err, serverErrors())
	}

	inner := tls.Client(client, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec // 测试目标站自签证书
	if err := inner.Handshake(); err != nil {
		t.Fatalf("经 Vision 的内层 TLS 1.3 握手: %v（服务端：%v）", err, serverErrors())
	}
	// 先来回几轮小消息：每轮回程是目标站的一条完整 TLS 记录，服务端写侧据此
	// 判定可以直通、发出 command=2（只有整记录才切，见 isCompleteTLSRecord）。
	// 合包模式不来回小消息：让 command=2 那一帧紧接着大块上行，一起到达。
	rounds := 5
	if coalesce {
		rounds = 0
	}
	for i := 0; i < rounds; i++ {
		ping := bytes.Repeat([]byte{byte('a' + i)}, 1000)
		if _, err := inner.Write(ping); err != nil {
			t.Fatalf("第 %d 轮写: %v（服务端：%v）", i, err, serverErrors())
		}
		pong := make([]byte, len(ping))
		if _, err := io.ReadFull(inner, pong); err != nil || !bytes.Equal(pong, ping) {
			t.Fatalf("第 %d 轮回显: %v（服务端：%v）", i, err, serverErrors())
		}
	}
	payload := make([]byte, 256<<10)
	_, _ = rand.Read(payload)
	go func() { _, _ = inner.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(inner, got); err != nil {
		t.Fatalf("内层 TLS 1.3 回显: %v（服务端：%v）", err, serverErrors())
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("内层 TLS 1.3 回显内容不符")
	}
	client.state.mu.Lock()
	enabled := client.state.enableXtls
	client.state.mu.Unlock()
	if !enabled || !client.sawCommand2 || (!coalesce && client.readDirect == nil) {
		t.Fatalf("没有走到直通：enableXtls=%v 上行command2=%v 下行直通=%v", enabled, client.sawCommand2, client.readDirect != nil)
	}

	// 直通后的流量照样按用户计量（计数在 VisionConn 之上，与外层无关）。
	deadline := time.Now().Add(3 * time.Second)
	for {
		current := adapter.sessions.peek(7501)
		if current.Upload >= int64(len(payload)) && current.Download >= int64(len(payload)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("直通后的流量没计上：%+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// 踢人：用户移出名单，直通中的连接也要立刻断——包括客户端不再读、服务端
	// 往裸 TCP 写到阻塞的连接（原先外层 Close 要先写 close_notify，卡满 5 秒）。
	go func() { _, _ = inner.Write(make([]byte, 32<<20)) }()
	time.Sleep(1500 * time.Millisecond)
	start := time.Now()
	if err := adapter.DelUsers([]string{id.String()}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("踢掉一条读不动的直通连接用了 %v", took)
	}
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err = io.Copy(io.Discard, raw)
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("移出名单后直通连接没断：%v", err)
	}
}
