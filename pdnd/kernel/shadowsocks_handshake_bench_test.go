package kernel

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// ssBenchConn 是只供握手基准与单测用的内存连接：Read 吐出预先构造的客户端
// 首包，Write 丢弃，RemoteAddr 固定，用来模拟「来源 IP 稳定」或「每次换 IP」。
type ssBenchConn struct {
	r      bytes.Reader
	remote net.Addr
}

func (c *ssBenchConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *ssBenchConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *ssBenchConn) Close() error                { return nil }
func (c *ssBenchConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8388}
}
func (c *ssBenchConn) RemoteAddr() net.Addr               { return c.remote }
func (c *ssBenchConn) SetDeadline(time.Time) error        { return nil }
func (c *ssBenchConn) SetReadDeadline(time.Time) error    { return nil }
func (c *ssBenchConn) SetWriteDeadline(time.Time) error   { return nil }
func (c *ssBenchConn) reset(wire []byte, remote net.Addr) { c.r.Reset(wire); c.remote = remote }

// buildSSClientRequest 按 SIP004 构造客户端首包：salt + 加密长度块 + 加密首块
// （目标 192.0.2.1:443 加 payload）。只用包内参考实现（标准 hkdf），与服务端
// 的派生路径相互独立，基准与单测因此也能对拍服务端的派生实现。
func buildSSClientRequest(tb testing.TB, method ssMethodSpec, password string, payload []byte) []byte {
	tb.Helper()
	master := deriveSSMasterKey(password, method.KeyLen)
	salt := make([]byte, method.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		tb.Fatal(err)
	}
	subkey, err := deriveSSSubkey(master, salt, method.KeyLen)
	if err != nil {
		tb.Fatal(err)
	}
	aead, err := method.NewAEAD(subkey)
	if err != nil {
		tb.Fatal(err)
	}
	plain := append([]byte{1, 192, 0, 2, 1, 0x01, 0xbb}, payload...)
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(plain)))
	out := append([]byte(nil), salt...)
	out = aead.Seal(out, makeSSNonce(0), length[:], nil)
	out = aead.Seal(out, makeSSNonce(1), plain, nil)
	return out
}

func newSSBenchAdapter(tb testing.TB, methodName string, users int) *shadowsocksAdapter {
	tb.Helper()
	value, err := newShadowsocksAdapter(InboundSpec{Config: core.InboundConfig{Protocol: "shadowsocks", Port: 8388, Raw: map[string]any{"method": methodName}}})
	if err != nil {
		tb.Fatal(err)
	}
	adapter := value.(*shadowsocksAdapter)
	batch := make([]core.User, users)
	for i := range batch {
		batch[i] = core.User{ID: int64(i + 1), UUID: ssBenchPassword(i)}
	}
	if err := adapter.AddUsers(batch); err != nil {
		tb.Fatal(err)
	}
	return adapter
}

func ssBenchPassword(i int) string { return fmt.Sprintf("bench-user-%06d-password", i) }

// BenchmarkShadowsocksHandshake 测 readRequest（认证 + 防重放 + 首块解密 +
// 回包 salt）的单次耗时：
//   - stable-ip：同一用户、同一来源 IP 反复建连（家宽/固定出口的常态）；
//   - cold-ip：每次随机用户、来源 IP 全不相同（缓存全不命中的最坏情况）。
//
// 每个请求的 salt 都不同，防重放表照常工作；请求在计时前构造好。
func BenchmarkShadowsocksHandshake(b *testing.B) {
	for _, methodName := range []string{"aes-128-gcm", "chacha20-ietf-poly1305"} {
		for _, users := range []int{100, 1000, 5000} {
			if methodName != "aes-128-gcm" && users != 5000 {
				continue
			}
			for _, mode := range []string{"stable-ip", "cold-ip"} {
				b.Run(fmt.Sprintf("%s/users=%d/%s", methodName, users, mode), func(b *testing.B) {
					adapter := newSSBenchAdapter(b, methodName, users)
					wires := make([][]byte, b.N)
					remotes := make([]net.Addr, b.N)
					var userPick [8]byte
					for i := range wires {
						user := users / 2
						remote := net.Addr(&net.TCPAddr{IP: net.IPv4(198, 51, 100, 7), Port: 40000})
						if mode == "cold-ip" {
							_, _ = rand.Read(userPick[:])
							user = int(binary.BigEndian.Uint64(userPick[:]) % uint64(users))
							remote = &net.TCPAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), Port: 40000}
						}
						wires[i] = buildSSClientRequest(b, adapter.method, ssBenchPassword(user), []byte("GET / HTTP/1.1\r\n\r\n"))
						remotes[i] = remote
					}
					conn := &ssBenchConn{}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						conn.reset(wires[i], remotes[i])
						_, stream, _, err := adapter.readRequest(conn)
						if err != nil || stream == nil {
							b.Fatalf("握手失败：%v", err)
						}
					}
				})
			}
		}
	}
}

var _ io.Reader = (*ssBenchConn)(nil)

// BenchmarkShadowsocksHandshakeParallel 在全部 P 上并发握手，体现分配与 GC
// 对多核吞吐的拖累（实测里 GC 协助占了两成以上）。
//   - stable-ip：64 个活跃用户、各自固定来源 IP 反复建连（计时前每人先握手一次）；
//   - cold-ip：5000 个用户轮流、每次都是新 IP。
func BenchmarkShadowsocksHandshakeParallel(b *testing.B) {
	const users, active = 5000, 64
	for _, mode := range []string{"stable-ip", "cold-ip"} {
		b.Run("aes-128-gcm/users=5000/"+mode, func(b *testing.B) {
			adapter := newSSBenchAdapter(b, "aes-128-gcm", users)
			pick := func(i int) int {
				if mode == "stable-ip" {
					return (i % active) * (users / active)
				}
				return (i * 7919) % users
			}
			stableAddr := func(user int) net.Addr {
				return &net.TCPAddr{IP: net.IPv4(172, 16, byte(user>>8), byte(user)), Port: 40000}
			}
			if mode == "stable-ip" {
				warm := &ssBenchConn{}
				for i := 0; i < active; i++ {
					warm.reset(buildSSClientRequest(b, adapter.method, ssBenchPassword(pick(i)), []byte("x")), stableAddr(pick(i)))
					if _, _, _, err := adapter.readRequest(warm); err != nil {
						b.Fatal(err)
					}
				}
			}
			wires := make([][]byte, b.N)
			for i := range wires {
				wires[i] = buildSSClientRequest(b, adapter.method, ssBenchPassword(pick(i)), []byte("GET / HTTP/1.1\r\n\r\n"))
			}
			var next atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				conn := &ssBenchConn{}
				for pb.Next() {
					i := int(next.Add(1) - 1)
					remote := net.Addr(&net.TCPAddr{IP: net.IPv4(10, byte(i>>16), byte(i>>8), byte(i)), Port: 40000})
					if mode == "stable-ip" {
						remote = stableAddr(pick(i))
					}
					conn.reset(wires[i], remote)
					if _, _, _, err := adapter.readRequest(conn); err != nil {
						b.Errorf("握手失败：%v", err)
						return
					}
				}
			})
		})
	}
}
