package node

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	nativekernel "github.com/aegispanel/nodeagent/kernel"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/panel"
	"github.com/google/uuid"
)

// 换凭据的端到端用例：真 NativeCore、真入站、真客户端握手。
//
// 用户重置订阅后面板只换 proxy_uuid、用户 ID（node_uid）不变。节点端必须做到：
// 旧凭据不在内核里（新握手被拒）、用旧凭据建立的已有连接被断开、新凭据能连、
// 同节点别的用户的连接不受牵连。面板的同一个 proxy_uuid 在各协议里有两种用法，
// 各取一个协议覆盖：UUID 型（vless / vmess / tuic 的 uuid）与口令型（trojan / ss /
// hysteria2 / anytls / socks 等的 password）。节点层与协议无关，换别的协议同理。

// tunnelDialer 用某个凭据经入站建一条到 target 的隧道；凭据被拒时返回 error。
type tunnelDialer func(t *testing.T, inboundPort int, credential string, target *net.TCPAddr) (net.Conn, error)

func TestCredentialRotationOnRealKernel(t *testing.T) {
	// 隧道目标是本机回环上的回声服务；出站默认拒绝私网目标，这里临时放开（本包测试不并行）。
	outbound.SetBlockPrivateDestinations(false)
	t.Cleanup(func() { outbound.SetBlockPrivateDestinations(true) })
	forms := []struct {
		name     string
		protocol string
		dial     tunnelDialer
	}{
		{name: "UUID 型（vless）", protocol: "vless", dial: dialVLESSTunnel},
		{name: "口令型（socks）", protocol: "socks", dial: dialSOCKSTunnel},
	}
	shapes := []struct {
		name  string
		delta func(id int64, fresh string) panel.StreamEvent
	}{
		{
			// 面板修复前的增量形状：同 ID 只给新记录。节点端单独也要挡住。
			name: "增量只给新记录",
			delta: func(id int64, fresh string) panel.StreamEvent {
				return panel.StreamEvent{Type: panel.EventSyncUserDelta, Added: []core.User{{ID: id, UUID: fresh}}}
			},
		},
		{
			// 面板现在的形状：同 ID 先删旧、再加新。
			name: "增量删旧加新",
			delta: func(id int64, fresh string) panel.StreamEvent {
				return panel.StreamEvent{Type: panel.EventSyncUserDelta, Removed: []int64{id}, Added: []core.User{{ID: id, UUID: fresh}}}
			},
		},
	}
	for _, form := range forms {
		for _, shape := range shapes {
			t.Run(form.name+"/"+shape.name, func(t *testing.T) {
				runRealKernelRotation(t, form.protocol, form.dial, shape.delta)
			})
		}
	}
}

func runRealKernelRotation(t *testing.T, protocol string, dial tunnelDialer, delta func(int64, string) panel.StreamEvent) {
	const (
		aliceID  = int64(7)
		aliceOld = "6f1e0c1a-2b3d-4c5e-8f70-000000000001"
		aliceNew = "6f1e0c1a-2b3d-4c5e-8f70-000000000002"
		bobID    = int64(8)
		bob      = "6f1e0c1a-2b3d-4c5e-8f70-000000000003"
	)
	echo := startEchoServer(t)
	kernel := &addHookKernel{NativeCore: newNativeKernel(t)}
	client := panel.New(panel.Options{BaseURL: "http://127.0.0.1:1", NodeID: "9", NodeType: protocol, Token: "token"})
	n := New(client, kernel, testLogger())
	port := freeTCPPort(t)
	if err := kernel.AddInbound(&core.InboundConfig{Tag: n.tag, Protocol: protocol, Listen: "127.0.0.1", Port: port}); err != nil {
		t.Fatal(err)
	}
	n.started = true
	if err := n.applyUsers([]core.User{{ID: aliceID, UUID: aliceOld}, {ID: bobID, UUID: bob}}); err != nil {
		t.Fatal(err)
	}
	n.userVersion = panel.UsersVersionKey(`"v1"`)

	oldTunnel := mustTunnel(t, dial, port, aliceOld, echo)
	bobTunnel := mustTunnel(t, dial, port, bob, echo)

	// 先删后加的可确定断言：新凭据一装进内核（AddUsers 返回、本次增量的其余内核操作
	// 还没做）就用它建一条隧道。内核按用户 ID 断线，若之后才删旧凭据，这条新凭据的
	// 隧道会被一并断掉；先删后加时它必须活着。
	var newTunnel net.Conn
	kernel.afterAdd = func(users []core.User) {
		for _, u := range users {
			if u.UUID == aliceNew && newTunnel == nil {
				newTunnel = mustTunnel(t, dial, port, aliceNew, echo)
			}
		}
	}
	ev := delta(aliceID, aliceNew)
	ev.FromVersion, ev.ToVersion = `"v1"`, `"v2"`
	n.applyStreamEvent(context.Background(), ev)
	kernel.afterAdd = nil
	if n.userVersion != panel.UsersVersionKey(`"v2"`) {
		t.Fatalf("增量没有被应用（userVersion=%q）", n.userVersion)
	}

	if !tunnelClosed(oldTunnel, 3*time.Second) {
		t.Fatal("用旧凭据建立的连接在换凭据后仍然存活：旧链接照样能用")
	}
	if conn, err := dial(t, port, aliceOld, echo); err == nil {
		_ = conn.Close()
		t.Fatal("换凭据后旧凭据仍能握手：旧 UUID 还留在内核里")
	}
	if newTunnel == nil {
		t.Fatal("增量没有把新凭据装进内核")
	}
	if !tunnelEchoes(newTunnel) {
		t.Fatal("新凭据装上后建立的连接被断开：内核操作不是先删旧凭据、再加新凭据")
	}
	_ = mustTunnel(t, dial, port, aliceNew, echo).Close()
	if !tunnelEchoes(bobTunnel) {
		t.Fatal("别的用户的连接被换凭据牵连断开")
	}
	_ = bobTunnel.Close()
}

// addHookKernel 是真 NativeCore，AddUsers 返回后回调 afterAdd（节点主循环同一 goroutine）。
type addHookKernel struct {
	*nativekernel.NativeCore
	afterAdd func([]core.User)
}

func (k *addHookKernel) AddUsers(tag string, users []core.User) error {
	if err := k.NativeCore.AddUsers(tag, users); err != nil {
		return err
	}
	if k.afterAdd != nil {
		k.afterAdd(users)
	}
	return nil
}

func mustTunnel(t *testing.T, dial tunnelDialer, port int, credential string, target *net.TCPAddr) net.Conn {
	t.Helper()
	conn, err := dial(t, port, credential, target)
	if err != nil {
		t.Fatalf("凭据 %s 建隧道失败：%v", credential, err)
	}
	if !tunnelEchoes(conn) {
		_ = conn.Close()
		t.Fatalf("凭据 %s 的隧道不通", credential)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// tunnelEchoes 发一行、读回同一行。
func tunnelEchoes(conn net.Conn) bool {
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && line == "ping\n"
}

// tunnelClosed 等服务端关掉这条连接：读到 EOF 或连接错误算关了，等到超时算没关。
func tunnelClosed(conn net.Conn, wait time.Duration) bool {
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 64)
	for {
		_, err := conn.Read(buf)
		if err == nil {
			continue
		}
		return !errors.Is(err, os.ErrDeadlineExceeded)
	}
}

func startEchoServer(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

func dialInbound(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("入站没有在监听：%v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	return conn
}

// dialVLESSTunnel 发一个 VLESS TCP 请求头（无 addons、IPv4 目标），读回响应头。
func dialVLESSTunnel(t *testing.T, port int, credential string, target *net.TCPAddr) (net.Conn, error) {
	t.Helper()
	id, err := uuid.Parse(credential)
	if err != nil {
		t.Fatal(err)
	}
	conn := dialInbound(t, port)
	req := []byte{0}
	req = append(req, id[:]...)
	req = append(req, 0, 1) // addons 长度 0；command=TCP
	req = binary.BigEndian.AppendUint16(req, uint16(target.Port))
	req = append(req, 1)
	req = append(req, target.IP.To4()...)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// dialSOCKSTunnel 走 SOCKS5 用户名/口令认证（两者都是凭据）再 CONNECT。
func dialSOCKSTunnel(t *testing.T, port int, credential string, target *net.TCPAddr) (net.Conn, error) {
	t.Helper()
	conn := dialInbound(t, port)
	fail := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return fail(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 2 {
		return fail(errors.New("SOCKS5 方法协商失败"))
	}
	auth := []byte{1, byte(len(credential))}
	auth = append(auth, credential...)
	auth = append(auth, byte(len(credential)))
	auth = append(auth, credential...)
	if _, err := conn.Write(auth); err != nil {
		return fail(err)
	}
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		return fail(errors.New("SOCKS5 认证被拒"))
	}
	req := []byte{5, 1, 0, 1}
	req = append(req, target.IP.To4()...)
	req = binary.BigEndian.AppendUint16(req, uint16(target.Port))
	if _, err := conn.Write(req); err != nil {
		return fail(err)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(conn, head); err != nil || head[1] != 0 {
		return fail(errors.New("SOCKS5 CONNECT 失败"))
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
