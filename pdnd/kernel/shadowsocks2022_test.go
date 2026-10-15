package kernel

// SS2022（SIP022 + SIP023）多用户入站 TCP 测试：配置校验、uPSK 向量、多用户互通、
// 大块读写、空响应 EOF、认证失败排空、删人踢线与 Upsert 不误伤其他用户。
// 数据均为虚构，客户端走 sing-shadowsocks shadowaead_2022。

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	ss2022ref "github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
)

// ss2022TestIPSK 返回固定的虚构 iPSK（第 i 字节 = byte(0x30+i)），长度 keyLen。
func ss2022TestIPSK(keyLen int) []byte {
	psk := make([]byte, keyLen)
	for i := range psk {
		psk[i] = byte(0x30 + i)
	}
	return psk
}

// ss2022TestKeyLen 按方法名给密钥长度（aes-128 16，其余 32）。
func ss2022TestKeyLen(method string) int {
	if strings.Contains(method, "aes-128") {
		return 16
	}
	return 32
}

// startSS2022Adapter 直接起一个 SS2022 适配器（监听 127.0.0.1，同端口 TCP+UDP，password = base64(ss2022TestIPSK)），
// 装好 users、Start 之前可经 tweak 改字段（如 headerTimeout），t.Cleanup 里 Close。
func startSS2022Adapter(t testing.TB, method string, plane DataPlane, tweak func(*ss2022Adapter), users ...core.User) *ss2022Adapter {
	t.Helper()
	allowLoopbackTargets(t)
	port := reserveTCPAndUDPPort(t)
	keyLen := ss2022TestKeyLen(method)
	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "ss2022-test", Protocol: "shadowsocks", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{
			"method":   method,
			"password": base64.StdEncoding.EncodeToString(ss2022TestIPSK(keyLen)),
		},
	}}
	value, err := newShadowsocksAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := value.(*ss2022Adapter)
	if !ok {
		t.Fatalf("adapter type=%T", value)
	}
	if err := a.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if tweak != nil {
		tweak(a)
	}
	if len(users) > 0 {
		if err := a.AddUsers(users); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := a.Start(ctx, spec, AdapterHooks{DataPlane: plane}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

// ss2022TestClientPSKs 是用户 uuid 在适配器 a 上的客户端密钥 {iPSK, uPSK}。
func ss2022TestClientPSKs(a *ss2022Adapter, uuid string) [][]byte {
	return [][]byte{a.ipsk, ss2022UserPSK(uuid, a.method.keyLen)}
}

func ss2022DialClient(t *testing.T, method string, psks [][]byte, addr string, dest M.Socksaddr, timeFunc func() time.Time) net.Conn {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	methodObj, err := ss2022ref.New(method, psks, timeFunc)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	conn, err := methodObj.DialConn(raw, dest)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	return conn
}

func TestSS2022UserPSKVectors(t *testing.T) {
	const uuid = "5f8c6a2e-3b1d-4c7a-9e2f-0a1b2c3d4e5f"
	want16 := "LSrMru66REBR94HS3SQJUQ=="
	want32 := "LSrMru66REBR94HS3SQJUYSSXOqFXz50ARjdL6oJuzM="
	if got := base64.StdEncoding.EncodeToString(ss2022UserPSK(uuid, 16)); got != want16 {
		t.Fatalf("keyLen=16: got %q want %q", got, want16)
	}
	if got := base64.StdEncoding.EncodeToString(ss2022UserPSK(uuid, 32)); got != want32 {
		t.Fatalf("keyLen=32: got %q want %q", got, want32)
	}
	upper := strings.ToUpper(uuid)
	if !bytes.Equal(ss2022UserPSK(uuid, 16), ss2022UserPSK(upper, 16)) {
		t.Fatal("大写 UUID 应与小写算出相同 uPSK")
	}
}

func TestSS2022ConfigValidation(t *testing.T) {
	valid128 := base64.StdEncoding.EncodeToString(ss2022TestIPSK(16))
	valid256 := base64.StdEncoding.EncodeToString(ss2022TestIPSK(32))
	cases := []struct {
		name       string
		method     string
		mutate     func(map[string]any)
		wantSubstr string
		wantOK     bool
	}{
		{name: "缺 password", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { delete(m, "password") }, wantSubstr: "password"},
		{name: "password 非 base64", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { m["password"] = "%%%" }, wantSubstr: "base64"},
		{name: "aes-256 配 16 字节", method: "2022-blake3-aes-256-gcm", mutate: func(m map[string]any) { m["password"] = valid128 }, wantSubstr: "32"},
		{name: "冒号两段", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { m["password"] = valid128 + ":" + valid128 }, wantSubstr: "password"},
		{name: "chacha20-poly1305", method: "2022-blake3-chacha20-poly1305", mutate: func(m map[string]any) { m["password"] = valid256 }, wantSubstr: "chacha20-poly1305"},
		{name: "network", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { m["network"] = "udp" }, wantSubstr: "不再接受 network 字段"},
		{name: "plugin", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { m["plugin"] = "obfs-local" }, wantSubstr: "plugin"},
		{name: "合法 aes-128", method: "2022-blake3-aes-128-gcm", mutate: func(m map[string]any) { m["password"] = valid128 }, wantOK: true},
		{name: "合法 aes-256", method: "2022-blake3-aes-256-gcm", mutate: func(m map[string]any) { m["password"] = valid256 }, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{"method": tc.method}
			if tc.method == "2022-blake3-aes-256-gcm" {
				raw["password"] = valid256
			} else if tc.method != "2022-blake3-chacha20-poly1305" {
				raw["password"] = valid128
			} else {
				raw["password"] = valid256
			}
			if tc.mutate != nil {
				tc.mutate(raw)
			}
			spec := InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Port: 443, Raw: raw}}
			adapter, newErr := newShadowsocksAdapter(spec)
			var valErr error
			if newErr == nil && adapter != nil {
				valErr = adapter.Validate(spec)
			}
			if tc.wantOK {
				if newErr != nil {
					t.Fatalf("newShadowsocksAdapter: %v", newErr)
				}
				if valErr != nil {
					t.Fatalf("Validate: %v", valErr)
				}
				return
			}
			msg := ""
			if newErr != nil {
				msg = newErr.Error()
			} else if valErr != nil {
				msg = valErr.Error()
			} else {
				t.Fatal("期望配置错误但被接受")
			}
			if tc.wantSubstr != "" && !strings.Contains(msg, tc.wantSubstr) {
				t.Fatalf("err=%q，期望包含 %q", msg, tc.wantSubstr)
			}
		})
	}
}

func TestSS2022MultiUserTCPInterop(t *testing.T) {
	for _, method := range []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"} {
		t.Run(method, func(t *testing.T) {
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
					go func(c net.Conn) {
						defer c.Close()
						_, _ = io.Copy(c, c)
					}(conn)
				}
			}()
			dest := M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()
			plane := &vlessTestPlane{target: dest}
			const uuid1 = "a1111111-1111-4111-8111-111111111101"
			const uuid2 = "a2222222-2222-4222-8222-222222222202"
			a := startSS2022Adapter(t, method, plane, nil,
				core.User{ID: 301, UUID: uuid1},
				core.User{ID: 302, UUID: uuid2},
			)
			addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
			payloads := []struct {
				uuid string
				data string
			}{
				{uuid1, "ss2022-user-301-payload"},
				{uuid2, "ss2022-user-302-other"},
			}
			var wg sync.WaitGroup
			for _, p := range payloads {
				wg.Add(1)
				go func(uuid, data string) {
					defer wg.Done()
					conn := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid), addr, dest, nil)
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := conn.Write([]byte(data)); err != nil {
						t.Error(err)
						return
					}
					got := make([]byte, len(data))
					if _, err := io.ReadFull(conn, got); err != nil {
						t.Error(err)
						return
					}
					if string(got) != data {
						t.Errorf("echo=%q want=%q", got, data)
					}
				}(p.uuid, p.data)
			}
			wg.Wait()
			assertDialedTarget(t, plane, upstream.Addr().String())
			traffic, err := a.SnapshotTraffic()
			if err != nil {
				t.Fatal(err)
			}
			byID := map[int64]core.UserTraffic{}
			for _, tr := range traffic {
				byID[tr.ID] = tr
			}
			for _, id := range []int64{301, 302} {
				tr, ok := byID[id]
				if !ok || tr.Upload == 0 || tr.Download == 0 {
					t.Fatalf("traffic id=%d: %+v", id, tr)
				}
			}
		})
	}
}

func TestSS2022TCPLargeChunks(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	dest := M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()
	const uuid = "b3333333-3333-4333-8333-333333333303"
	a := startSS2022Adapter(t, method, &vlessTestPlane{target: dest}, nil, core.User{ID: 401, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	conn := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid), addr, dest, nil)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	payload := randomBytes(200 * 1024)
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("读回 %d 字节，与发送不一致", len(got))
	}
}

// ss2022ClosingPlane 的上游 TCP 在 Accept 后立刻关闭，用于测空响应与 EOF。
type ss2022ClosingPlane struct{}

func (ss2022ClosingPlane) DialTCP(_ context.Context, _ route.Meta, _ M.Socksaddr) (net.Conn, error) {
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func (ss2022ClosingPlane) ListenUDP(_ context.Context, _ route.Meta, _ M.Socksaddr) (net.PacketConn, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestSS2022TCPEmptyResponseThenEOF(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	const uuid = "c4444444-4444-4444-8444-444444444404"
	a := startSS2022Adapter(t, method, ss2022ClosingPlane{}, nil, core.User{ID: 402, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	conn := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid), addr, M.ParseSocksaddrHostPort("127.0.0.1", 443), nil)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("ReadAll err=%v", err)
	}
	if len(got) != 0 {
		t.Fatalf("期望 0 字节响应，收到 %d", len(got))
	}
}

func ss2022RecordClientFirstWrite(t *testing.T, method string, psks [][]byte, addr string, dest M.Socksaddr, timeFunc func() time.Time) []byte {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingConn{Conn: raw}
	methodObj, err := ss2022ref.New(method, psks, timeFunc)
	if err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	stream, err := methodObj.DialConn(rec, dest)
	if err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := stream.Write([]byte("x")); err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	_ = stream.Close()
	_ = rec.Close()
	return rec.bytes()
}

func TestSS2022TCPUnknownUserDrainsUntilPeerClose(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	const uuid = "d5555555-5555-4555-8555-555555555505"
	a := startSS2022Adapter(t, method, &vlessTestPlane{}, func(ad *ss2022Adapter) {
		ad.headerTimeout = drainTestHeaderTimeout
	}, core.User{ID: 1, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	unknownPSK := [][]byte{a.ipsk, ss2022UserPSK("e6666666-6666-4666-8666-666666666606", 16)}
	payload := ss2022RecordClientFirstWrite(t, method, unknownPSK, addr, M.ParseSocksaddrHostPort("127.0.0.1", 443), nil)
	probeUntilPeerClose(t, addr, payload, 3*drainTestHeaderTimeout)
}

func TestSS2022TCPReplayedSaltDrainsUntilPeerClose(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	const uuid = "f7777777-7777-4777-8777-777777777707"
	echoTarget := M.SocksaddrFromNet(startMultiEcho(t)).Unwrap()
	a := startSS2022Adapter(t, method, &vlessTestPlane{target: echoTarget}, func(ad *ss2022Adapter) {
		ad.headerTimeout = drainTestHeaderTimeout
	}, core.User{ID: 1, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingConn{Conn: raw}
	methodObj, err := ss2022ref.New(method, ss2022TestClientPSKs(a, uuid), nil)
	if err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	stream, err := methodObj.DialConn(rec, echoTarget)
	if err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	_ = stream.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := stream.Write([]byte("first-flight")); err != nil {
		_ = rec.Close()
		t.Fatal(err)
	}
	echo := make([]byte, len("first-flight"))
	if _, err := io.ReadFull(stream, echo); err != nil || string(echo) != "first-flight" {
		_ = rec.Close()
		t.Fatalf("合法连接 echo=%q err=%v", echo, err)
	}
	_ = stream.Close()
	_ = rec.Close()
	probeUntilPeerClose(t, addr, rec.bytes(), 3*drainTestHeaderTimeout)
}

func TestSS2022TCPStaleTimestampDrainsUntilPeerClose(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	const uuid = "08888888-8888-4888-8888-888888888808"
	a := startSS2022Adapter(t, method, &vlessTestPlane{}, func(ad *ss2022Adapter) {
		ad.headerTimeout = drainTestHeaderTimeout
	}, core.User{ID: 1, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	staleTime := func() time.Time { return time.Now().Add(-2 * time.Minute) }
	payload := ss2022RecordClientFirstWrite(t, method, ss2022TestClientPSKs(a, uuid), addr, M.ParseSocksaddrHostPort("127.0.0.1", 443), staleTime)
	probeUntilPeerClose(t, addr, payload, 3*drainTestHeaderTimeout)
}

func TestSS2022TCPGarbageDrainsUntilPeerClose(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
	const uuid = "19999999-9999-4999-8999-999999999909"
	a := startSS2022Adapter(t, method, &vlessTestPlane{}, func(ad *ss2022Adapter) {
		ad.headerTimeout = drainTestHeaderTimeout
	}, core.User{ID: 1, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	probeUntilPeerClose(t, addr, randomBytes(80), 3*drainTestHeaderTimeout)
}

func TestSS2022TCPDelUserKicksConnection(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	dest := M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()
	const uuid = "1aaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa0a"
	a := startSS2022Adapter(t, method, &vlessTestPlane{target: dest}, nil, core.User{ID: 501, UUID: uuid})
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	conn := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid), addr, dest, nil)
	if _, err := conn.Write([]byte("kick-me")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("kick-me"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if err := a.DelUsers([]string{uuid}); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("删用户后旧连接应读到错误")
	}
	conn2 := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid), addr, dest, nil)
	defer conn2.Close()
	_ = conn2.SetReadDeadline(time.Now().Add(1 * time.Second))
	if _, err := conn2.Write([]byte("retry")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn2, make([]byte, len("retry"))); err == nil {
		t.Fatal("删用户后新连接应无法回显")
	}
}

func TestSS2022UpsertKeepsOtherUsers(t *testing.T) {
	method := "2022-blake3-aes-128-gcm"
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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	dest := M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()
	const uuid1 = "2bbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb0b"
	const uuid2 = "3ccccccc-cccc-4ccc-8ccc-cccccccccc0c"
	a := startSS2022Adapter(t, method, &vlessTestPlane{target: dest}, nil,
		core.User{ID: 601, UUID: uuid1, SpeedLimit: 0},
		core.User{ID: 602, UUID: uuid2, SpeedLimit: 0},
	)
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(a.spec.Config.Port))
	conn2 := ss2022DialClient(t, method, ss2022TestClientPSKs(a, uuid2), addr, dest, nil)
	defer conn2.Close()
	_ = conn2.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn2.Write([]byte("before-upsert")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("before-upsert"))
	if _, err := io.ReadFull(conn2, got); err != nil || string(got) != "before-upsert" {
		t.Fatalf("upsert 前 user2 echo err=%v", err)
	}
	if err := a.UpsertUsers([]core.User{{ID: 601, UUID: uuid1, SpeedLimit: 1_000_000}}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn2.Write([]byte("after-upsert")); err != nil {
		t.Fatal(err)
	}
	got2 := make([]byte, len("after-upsert"))
	if _, err := io.ReadFull(conn2, got2); err != nil || string(got2) != "after-upsert" {
		t.Fatalf("upsert 后 user2 应仍能回显: got=%q err=%v", got2, err)
	}
}
