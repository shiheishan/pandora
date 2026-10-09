package kernel

// 各协议适配器的在线设备接线：经真实 NativeCore 把客户端连到本机回显上游，
// 验证每个协议都走 onlineDevices 的引用计数（跟踪器本身的语义见 online_devices_test.go）：
//   - 别的 IP 占着唯一名额时本机连接被拒；
//   - 设备上限 1 时同 IP 两条连接都放行（同 IP 多连接不额外占名额）；
//   - 两条关一条，用户仍在线；全部关掉才离线。
// QUIC 系与 AnyTLS 的用例在 online_devices_wiring_quic_test.go。用户与口令全是虚构的。

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	ss2022ref "github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"

	"github.com/aegispanel/nodeagent/core"
	singcore "github.com/aegispanel/nodeagent/core/sing"
)

// deviceWiringCase 描述一个协议的入站怎么配、客户端怎么连。
type deviceWiringCase struct {
	name  string
	proto string
	// raw 是入站的 Raw 配置；uuid 是该协议认的用户口令。
	raw  func(t *testing.T) map[string]any
	uuid string
	// open 建一条经代理到 target 的连接并回显一次确认打通；被拒或不通返回错误。
	open func(env deviceWiringEnv) (io.Closer, error)
}

type deviceWiringEnv struct {
	t       *testing.T
	port    int
	user    core.User
	target  *net.TCPAddr
	adapter Adapter
}

// 「别的设备」用的文档保留地址（TEST-NET-2），直接登记进跟踪器模拟另一台设备在线。
const deviceWiringOtherIP = "198.51.100.7"

func TestOnlineDevicesWiring(t *testing.T) {
	cases := append(deviceWiringTCPCases(), deviceWiringQUICCases()...)
	// UDP 路径（hy2 / TUIC / SS / SS2022 / SOCKS / Trojan）见 online_devices_wiring_udp_test.go。
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runDeviceWiring(t, tc) })
	}
}

func runDeviceWiring(t *testing.T, tc deviceWiringCase) {
	echo := startLifecycleEcho(t, false)
	user := core.User{ID: 7100, UUID: tc.uuid, DeviceLimit: 1}
	raw := map[string]any{}
	if tc.raw != nil {
		raw = tc.raw(t)
	}
	c, port, tag := startLifecycleCore(t, lifecycleProto{name: tc.proto, raw: raw}, []core.User{user})
	in, err := c.getInbound(tag)
	if err != nil {
		t.Fatal(err)
	}
	in.mu.RLock()
	adapter := in.adapter
	in.mu.RUnlock()
	devices := devicesOf(t, adapter)
	env := deviceWiringEnv{t: t, port: port, user: user, target: echo.addr(), adapter: adapter}
	online := func() []string { return adapter.OnlineIPs()[user.ID] }

	// 1. 别的 IP 占着唯一名额：本机来的连接被拒，被拒的连接不留记录。
	if !devices.enter(user, deviceWiringOtherIP) {
		t.Fatal("空表时登记别的 IP 被拒")
	}
	if conn, err := tc.open(env); err == nil {
		_ = conn.Close()
		t.Fatal("名额被别的 IP 占着时，本机连接仍被放行")
	}
	devices.leave(user, deviceWiringOtherIP)
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		live, ips := liveSessionsOf(adapter), online()
		return live == 0 && len(ips) == 0, fmt.Sprintf("会话=%d 在线=%v", live, ips)
	}); !ok {
		t.Fatalf("被拒的连接留下了记录：%s", why)
	}

	// 2. 设备上限 1：同 IP 的两条连接都放行，且只算一台设备。
	c1, err := tc.open(env)
	if err != nil {
		t.Fatalf("第一条连接：%v", err)
	}
	defer c1.Close()
	c2, err := tc.open(env)
	if err != nil {
		t.Fatalf("同 IP 第二条连接被拒（同 IP 多连接不该额外占名额）：%v", err)
	}
	defer c2.Close()
	if got := online(); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("两条连接后在线 IP=%v，期望 [127.0.0.1]", got)
	}
	if devices.enter(user, deviceWiringOtherIP) {
		t.Fatal("本机已占满名额时，别的 IP 仍被放行")
	}

	// 3. 关一条：剩下那条还在，用户必须仍在线。
	before := liveSessionsOf(adapter)
	_ = c1.Close()
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		live := liveSessionsOf(adapter)
		return live < before, fmt.Sprintf("会话=%d（关之前 %d）", live, before)
	}); !ok {
		t.Fatalf("关掉一条后服务端没收尾：%s", why)
	}
	// 有的协议先关会话、后撤在线登记：留一点时间让收尾跑完，再断言仍在线。
	time.Sleep(150 * time.Millisecond)
	if got := online(); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("同 IP 两条连接关掉一条后在线 IP=%v，期望仍是 [127.0.0.1]", got)
	}

	// 4. 全部关掉才离线。
	_ = c2.Close()
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		live, ips := liveSessionsOf(adapter), online()
		return live == 0 && len(ips) == 0, fmt.Sprintf("会话=%d 在线=%v", live, ips)
	}); !ok {
		t.Fatalf("全部关掉后仍在线：%s", why)
	}
}

// devicesOf 取适配器内嵌的在线设备跟踪器。
func devicesOf(t *testing.T, a Adapter) *onlineDevices {
	t.Helper()
	switch v := a.(type) {
	case *vlessAdapter:
		return &v.online
	case *vmessAdapter:
		return &v.online
	case *trojanAdapter:
		return &v.online
	case *shadowsocksAdapter:
		return &v.online
	case *ss2022Adapter:
		return &v.online
	case *proxyAdapter:
		return &v.online
	case *naiveAdapter:
		return &v.online
	case *hysteria2Adapter:
		return &v.online
	case *tuicAdapter:
		return &v.online
	case *juicityAdapter:
		return &v.online
	case *anyTLSAdapter:
		return &v.online
	case interface{ innerAdapter() Adapter }:
		return devicesOf(t, v.innerAdapter())
	}
	t.Fatalf("适配器 %T 没有接在线设备跟踪器", a)
	return nil
}

func deviceWiringTCPCases() []deviceWiringCase {
	ss2022PSK := make([]byte, 16)
	_, _ = rand.Read(ss2022PSK)
	return []deviceWiringCase{
		{name: "vless", proto: "vless", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7101",
			raw: func(*testing.T) map[string]any { return map[string]any{"network": "tcp"} }, open: lifecycleOpen(dialLifecycleVLESS)},
		{name: "vmess", proto: "vmess", uuid: "6b1f6f2e-2a43-4c38-9f36-6f7f3c0a7102",
			raw: func(*testing.T) map[string]any { return map[string]any{"security": "aes-128-gcm"} }, open: lifecycleOpen(dialLifecycleVMess)},
		{name: "trojan", proto: "trojan", uuid: "device-wiring-trojan", open: lifecycleOpen(dialLifecycleTrojan)},
		{name: "shadowsocks", proto: "shadowsocks", uuid: "device-wiring-ss",
			raw: func(*testing.T) map[string]any { return map[string]any{"method": "aes-128-gcm"} }, open: lifecycleOpen(dialLifecycleSS)},
		{name: "shadowsocks2022", proto: "shadowsocks", uuid: "device-wiring-ss2022",
			raw: func(*testing.T) map[string]any {
				return map[string]any{"method": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(ss2022PSK)}
			},
			open: lifecycleOpen(func(port int, _ core.User, target *net.TCPAddr) (net.Conn, error) {
				return dialDeviceWiringSS2022(port, ss2022PSK, target)
			})},
		{name: "socks", proto: "socks", uuid: "device-wiring-socks", open: lifecycleOpen(dialDeviceWiringSOCKS5)},
		{name: "http", proto: "http", uuid: "device-wiring-http", open: lifecycleOpen(dialDeviceWiringHTTPConnect)},
		{name: "naive", proto: "naive", uuid: "device-wiring-naive", raw: deviceWiringTLSRaw(map[string]any{"tls": true}), open: openDeviceWiringNaive},
	}
}

// lifecycleOpen 把只负责拨号的客户端包成「拨号 + 回显一次」。
func lifecycleOpen(dial func(port int, user core.User, target *net.TCPAddr) (net.Conn, error)) func(deviceWiringEnv) (io.Closer, error) {
	return func(env deviceWiringEnv) (io.Closer, error) {
		conn, err := dial(env.port, env.user, env.target)
		if err != nil {
			return nil, err
		}
		if err := echoOnce(conn, "device-wiring"); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// deviceWiringTLSRaw 在 base 上加一张临时自签证书。
func deviceWiringTLSRaw(base map[string]any) func(t *testing.T) map[string]any {
	return func(t *testing.T) map[string]any {
		certPath, keyPath := testXHTTPServerCertFiles(t)
		raw := map[string]any{"cert_path": certPath, "key_path": keyPath}
		for k, v := range base {
			raw[k] = v
		}
		return raw
	}
}

func dialDeviceWiringSS2022(port int, psk []byte, target *net.TCPAddr) (net.Conn, error) {
	raw, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	method, err := ss2022ref.New("2022-blake3-aes-128-gcm", [][]byte{psk}, nil)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	conn, err := method.DialConn(raw, M.SocksaddrFromNet(target))
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

// bufferedConn 让握手时 bufio 已读进来的字节不丢。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func dialDeviceWiringSOCKS5(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	conn, _, err := socks5DeviceWiringRequest(port, user, 1, target.IP, target.Port)
	return conn, err
}

// socks5DeviceWiringRequest 做完 SOCKS5 用户名口令认证并发出 cmd（1 = CONNECT，
// 3 = UDP ASSOCIATE），返回读掉应答后的连接与 10 字节应答。
func socks5DeviceWiringRequest(port int, user core.User, cmd byte, ip net.IP, targetPort int) (net.Conn, []byte, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 3*time.Second)
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	fail := func(err error) (net.Conn, []byte, error) { _ = conn.Close(); return nil, nil, err }
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return fail(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(reader, method[:]); err != nil || method[1] != 2 {
		return fail(fmt.Errorf("socks method=%v err=%v", method, err))
	}
	auth := []byte{1, byte(len(user.UUID))}
	auth = append(auth, user.UUID...)
	auth = append(auth, byte(len(user.UUID)))
	auth = append(auth, user.UUID...)
	if _, err := conn.Write(auth); err != nil {
		return fail(err)
	}
	var status [2]byte
	if _, err := io.ReadFull(reader, status[:]); err != nil || status[1] != 0 {
		return fail(fmt.Errorf("socks auth=%v err=%v", status, err))
	}
	request := []byte{5, cmd, 0, 1}
	request = append(request, ip.To4()...)
	request = append(request, byte(targetPort>>8), byte(targetPort))
	if _, err := conn.Write(request); err != nil {
		return fail(err)
	}
	response := make([]byte, 10)
	if _, err := io.ReadFull(reader, response); err != nil || response[1] != 0 {
		return fail(fmt.Errorf("socks response=%v err=%v", response, err))
	}
	_ = conn.SetDeadline(time.Time{})
	return bufferedConn{Conn: conn, r: reader}, response, nil
}

func dialDeviceWiringHTTPConnect(port int, user core.User, target *net.TCPAddr) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 3*time.Second)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte(user.UUID + ":" + user.UUID))
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("status=%s", response.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	return bufferedConn{Conn: conn, r: reader}, nil
}

func openDeviceWiringNaive(env deviceWiringEnv) (io.Closer, error) {
	raw, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(env.port)), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http2.NextProtoTLS}}) //nolint:gosec -- ephemeral test certificate.
	if err != nil {
		return nil, err
	}
	clientConn, err := (&http2.Transport{}).NewClientConn(raw)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	pipeReader, pipeWriter := io.Pipe()
	req, err := http.NewRequest(http.MethodConnect, "https://127.0.0.1/", pipeReader)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	req.Host = env.target.String()
	req.Header.Set("Padding", "~device-wiring-padding~")
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(env.user.UUID+":"+env.user.UUID)))
	// 请求的 context 取消会连带重置已建好的流，所以用底层连接的截止时间给握手限时。
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	resp, err := clientConn.RoundTrip(req)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		_ = raw.Close()
		return nil, fmt.Errorf("status=%s", resp.Status)
	}
	conn := singcore.NewNaiveClientConn(resp.Body, pipeWriter, naiveTestFlusher{}, raw.RemoteAddr())
	if err := echoOnce(conn, "device-wiring"); err != nil {
		_ = conn.Close()
		_ = raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return closerFunc(func() error { return errors.Join(conn.Close(), raw.Close()) }), nil
}
