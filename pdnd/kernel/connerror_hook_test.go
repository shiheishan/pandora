package kernel

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/http2"
)

// 每个协议至少有一条失败路径真的走到 AdapterHooks.OnConnError。
//
// 这组测试钉住的是「接没接上」：原来只有 vless / trojan 的 TCP 路径会上报，
// 其余协议的失败都是 conn.Close() 走人。凭据、地址全部是虚构的。

// ============================================================
//  夹具
// ============================================================

type connErrorRecorder struct{ ch chan ConnError }

func newConnErrorRecorder() *connErrorRecorder {
	return &connErrorRecorder{ch: make(chan ConnError, 256)}
}

func (r *connErrorRecorder) hook(ev ConnError) {
	select {
	case r.ch <- ev:
	default:
	}
}

// wait 等一条协议、阶段、分类都对得上的失败；category 为空表示不限分类。
func (r *connErrorRecorder) wait(t *testing.T, protocol, stage, category string) ConnError {
	t.Helper()
	timeout := time.After(10 * time.Second)
	var seen []string
	for {
		select {
		case ev := <-r.ch:
			got := classifyConnError(ev.Err)
			if ev.Protocol == protocol && ev.Stage == stage && (category == "" || got == category) {
				return ev
			}
			seen = append(seen, fmt.Sprintf("%s/%s/%s: %v", ev.Protocol, ev.Stage, got, ev.Err))
		case <-timeout:
			t.Fatalf("没等到 %s/%s/%s 的上报，收到过: %v", protocol, stage, category, seen)
		}
	}
}

// refusePlane 让所有出站都失败，用来走「鉴权已过、拨目标失败」那条路径。
type refusePlane struct{}

var errFictionalRefusal = errors.New("fictional upstream refusal")

func (refusePlane) DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error) {
	return nil, errFictionalRefusal
}

func (refusePlane) ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error) {
	return nil, errFictionalRefusal
}

// startHookedAdapter 经默认注册表起一个入站，钩子接到 recorder 上。
func startHookedAdapter(t *testing.T, protocol string, port int, raw map[string]any, plane DataPlane, users ...core.User) (Adapter, *connErrorRecorder) {
	t.Helper()
	if raw == nil {
		raw = map[string]any{}
	}
	spec := InboundSpec{Config: core.InboundConfig{Tag: "hook-" + protocol, Protocol: protocol, Listen: "127.0.0.1", Port: port, Raw: raw}}
	adapter, err := NewDefaultAdapterRegistry().New(spec)
	if err != nil {
		t.Fatal(err)
	}
	rec := newConnErrorRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: plane, OnConnError: rec.hook}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if len(users) > 0 {
		if err := adapter.AddUsers(users); err != nil {
			t.Fatal(err)
		}
	}
	return adapter, rec
}

const hookUserUUID = "3b2a1c0d-9e8f-4a7b-8c6d-5e4f3a2b1c0d"

var hookUser = core.User{ID: 501, UUID: hookUserUUID}

// junk16 是一段固定的、不属于任何用户的 16 字节。
var junk16 = []byte{0xa5, 0x5a, 0x0f, 0xf0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc}

// ============================================================
//  TCP 承载的协议
// ============================================================

func TestConnErrorHookVLESSUnknownUUID(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "vless", port, nil, refusePlane{}, hookUser)
	sendRaw(t, port, append([]byte{vlessVersion}, junk16...))
	ev := rec.wait(t, "vless", StageSession, connErrAuth)
	if ev.Tag != "hook-vless" || ev.Remote == nil {
		t.Fatalf("上报缺 tag 或对端地址: %+v", ev)
	}
}

func TestConnErrorHookVMessUnknownAuthID(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "vmess", port, nil, refusePlane{}, hookUser)
	sendRaw(t, port, append(append([]byte{}, junk16...), junk16...))
	rec.wait(t, "vmess", StageSession, connErrAuth)
}

func TestConnErrorHookTrojanWrongPassword(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "trojan", port, nil, refusePlane{}, hookUser)
	proof := sha256.Sum224([]byte("not-the-password"))
	sendRaw(t, port, []byte(hex.EncodeToString(proof[:])+"\r\n"))
	rec.wait(t, "trojan", StageSession, connErrAuth)
}

func TestConnErrorHookShadowsocksWrongKey(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "shadowsocks", port, map[string]any{"method": "aes-128-gcm"}, refusePlane{}, hookUser)
	sendRaw(t, port, []byte(strings.Repeat(string(junk16), 4)))
	rec.wait(t, "shadowsocks", StageSession, connErrAuth)
}

func TestConnErrorHookShadowsocks2022WrongPSK(t *testing.T) {
	port := reserveTCPPort(t)
	psk := base64.StdEncoding.EncodeToString(junk16)
	_, rec := startHookedAdapter(t, "shadowsocks", port, map[string]any{"method": "2022-blake3-aes-128-gcm", "password": psk}, refusePlane{})
	sendRaw(t, port, []byte(strings.Repeat(string(junk16), 4)))
	rec.wait(t, "shadowsocks", StageSession, connErrAuth)
}

func TestConnErrorHookShadowTLSNotAClientHello(t *testing.T) {
	port := reserveTCPPort(t)
	raw := map[string]any{"password": "fictional-outer", "server": "127.0.0.1:9", "strict": false}
	_, rec := startHookedAdapter(t, "shadowtls", port, raw, refusePlane{})
	// 一条完整但不是 ClientHello 的 TLS 记录：外层伪装握手在取 SNI 时失败。
	sendRaw(t, port, []byte{0x17, 3, 3, 0, 4, 1, 2, 3, 4})
	ev := rec.wait(t, "shadowtls", StageTLSHandshake, "")
	if ev.Tag != "hook-shadowtls" {
		t.Fatalf("外层握手失败没记在 shadowtls 入站名下: %+v", ev)
	}
}

func TestConnErrorHookSOCKS5WrongCredentials(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "socks", port, nil, refusePlane{}, hookUser)
	conn := dialHookPort(t, port)
	defer conn.Close()
	mustWrite(t, conn, []byte{5, 1, 2})
	mustRead(t, conn, 2)
	name, password := "intruder", "wrong"
	request := append([]byte{1, byte(len(name))}, name...)
	request = append(append(request, byte(len(password))), password...)
	mustWrite(t, conn, request)
	rec.wait(t, "socks", StageSession, connErrAuth)
}

func TestConnErrorHookHTTPProxyWrongCredentials(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "http", port, nil, refusePlane{}, hookUser)
	credentials := base64.StdEncoding.EncodeToString([]byte("intruder:wrong"))
	sendRaw(t, port, []byte("CONNECT target.test:80 HTTP/1.1\r\nHost: target.test:80\r\nProxy-Authorization: Basic "+credentials+"\r\n\r\n"))
	rec.wait(t, "http", StageSession, connErrAuth)
}

func TestConnErrorHookSOCKSUpstreamRefusal(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "socks", port, nil, refusePlane{}, hookUser)
	conn := dialHookPort(t, port)
	defer conn.Close()
	mustWrite(t, conn, []byte{5, 1, 2})
	mustRead(t, conn, 2)
	request := append([]byte{1, byte(len(hookUserUUID))}, hookUserUUID...)
	request = append(append(request, byte(len(hookUserUUID))), hookUserUUID...)
	mustWrite(t, conn, request)
	mustRead(t, conn, 2)
	mustWrite(t, conn, []byte{5, 1, 0, 1, 192, 0, 2, 1, 0, 80})
	ev := rec.wait(t, "socks", StageSession, "")
	if !errors.Is(ev.Err, errFictionalRefusal) {
		t.Fatalf("拨号失败没有原样上报: %v", ev.Err)
	}
}

func TestConnErrorHookNaiveNonConnect(t *testing.T) {
	port := reserveTCPPort(t)
	certPath, keyPath := testXHTTPServerCertFiles(t)
	raw := map[string]any{"tls": true, "cert_path": certPath, "key_path": keyPath}
	_, rec := startHookedAdapter(t, "naive", port, raw, refusePlane{}, hookUser)
	tlsConn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http2.NextProtoTLS}}) //nolint:gosec -- ephemeral test certificate.
	if err != nil {
		t.Fatal(err)
	}
	defer tlsConn.Close()
	client, err := (&http2.Transport{}).NewClientConn(tlsConn)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://probe.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	rec.wait(t, "naive", StageSession, connErrProtocol)
}

func TestConnErrorHookAnyTLSUnknownPassword(t *testing.T) {
	port := reserveTCPPort(t)
	_, rec := startHookedAdapter(t, "anytls", port, nil, refusePlane{}, hookUser)
	wrong := sha256.Sum256([]byte("not-the-password"))
	sendRaw(t, port, append(wrong[:], 0, 0))
	rec.wait(t, "anytls", StageSession, "")
}

// ============================================================
//  小工具
// ============================================================

func dialHookPort(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func mustWrite(t *testing.T, conn net.Conn, payload []byte) {
	t.Helper()
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	read := 0
	for read < n {
		m, err := conn.Read(buf[read:])
		if err != nil {
			t.Fatalf("读 %d 字节只读到 %d: %v", n, read, err)
		}
		read += m
	}
	return buf
}
