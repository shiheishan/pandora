//go:build linux

package hysteria2

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"net"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/net/ipv4"
)

// 服务端的批量 Salamander（Pandora 改动，上游没有）。
//
// 上游 SalamanderPacketConn 只是个 net.PacketConn 包装：quic-go 认不出它是 UDP
// socket，退回逐包 ReadFrom / WriteTo，没有 recvmmsg、没有 GSO，开混淆的入站每
// Gbps 要多花约一核。这里让混淆层自己实现 quic-go 认的接口：
//
//   - OOBCapablePacketConn（ReadMsgUDP / WriteMsgUDP / SyscallConn / SetReadBuffer）
//     与 batchConn（ReadBatch）：quic-go 于是照常走 recvmmsg 批量收、GSO 合包发；
//   - ReadBatch 收完一批后逐包去盐解混淆（原地，与 ReadFrom 同一套算法）；
//   - WriteMsgUDP 遇到 GSO（控制信息里有 UDP_SEGMENT，b 是若干等长段拼接）时，
//     给每一段各加 8 字节随机盐、各自异或，段长改为原段长 + 8 后整条交给内核。
//
// 线上每个 UDP 包与逐包混淆逐字节同格式（盐 8 字节 + 异或后的 QUIC 包）。
// 只暴露上面这些方法，不内嵌 *net.UDPConn：任何未混淆的收发方法都不透出。

const udpSegmentOption = 103 // linux/udp.h 的 UDP_SEGMENT

// quic-go 认这两个接口才走批量收与 GSO（sys_conn.go 的 OOBCapablePacketConn 与
// sys_conn_oob.go 的 batchConn）。
var _ interface {
	ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error)
	WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (n, oobn int, err error)
	SyscallConn() (syscall.RawConn, error)
	SetReadBuffer(int) error
	ReadBatch([]ipv4.Message, int) (int, error)
	net.PacketConn
} = (*batchSalamanderConn)(nil)

type batchSalamanderConn struct {
	conn     *net.UDPConn
	batch    *ipv4.PacketConn
	password []byte
	// readKey 只在收方向用；quic-go 只有一个收包 goroutine。
	readKey []byte
	// writeBuffers 是 GSO 发送时拼盐后的输出缓冲。
	writeBuffers sync.Pool
}

// newServerSalamanderConn 给服务端监听 socket 套上混淆：Linux 上的裸 UDP socket
// 用批量实现，其余退回上游逐包实现。
func newServerSalamanderConn(conn net.PacketConn, password []byte) net.PacketConn {
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		return NewSalamanderConn(conn, password)
	}
	c := &batchSalamanderConn{conn: udp, batch: ipv4.NewPacketConn(udp), password: password}
	c.readKey = make([]byte, 0, len(password)+salamanderSaltLen)
	c.writeBuffers.New = func() any {
		b := make([]byte, 0, 1<<16+64*salamanderSaltLen)
		return &b
	}
	return c
}

// salamanderKey 算 blake2b(password ‖ salt)。scratch 至少能装下两者。
func salamanderKey(scratch, password, salt []byte) [blake2b.Size256]byte {
	scratch = append(append(scratch[:0], password...), salt...)
	return blake2b.Sum256(scratch)
}

// salamanderXOR 把 src 按 key 循环异或进 dst（dst 与 src 可以是同一缓冲里错开
// salamanderSaltLen 的两段，所以按 32 字节一块先拷出再异或）。
func salamanderXOR(dst, src []byte, key *[blake2b.Size256]byte) {
	var block [blake2b.Size256]byte
	for off := 0; off < len(src); off += blake2b.Size256 {
		end := min(off+blake2b.Size256, len(src))
		n := copy(block[:], src[off:end])
		subtle.XORBytes(dst[off:end], block[:n], key[:n])
	}
}

// deobfuscate 原地去盐解混淆，返回明文长度。不超过盐长的包原样交给 quic-go
// （与上游 ReadFrom 相同，quic-go 会当坏包丢掉）。
func (c *batchSalamanderConn) deobfuscate(p []byte) int {
	if len(p) <= salamanderSaltLen {
		return len(p)
	}
	key := salamanderKey(c.readKey, c.password, p[:salamanderSaltLen])
	salamanderXOR(p, p[salamanderSaltLen:], &key)
	return len(p) - salamanderSaltLen
}

func (c *batchSalamanderConn) ReadBatch(ms []ipv4.Message, flags int) (int, error) {
	n, err := c.batch.ReadBatch(ms, flags)
	for i := 0; i < n; i++ {
		ms[i].N = c.deobfuscate(ms[i].Buffers[0][:ms[i].N])
	}
	return n, err
}

func (c *batchSalamanderConn) ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error) {
	n, oobn, flags, addr, err = c.conn.ReadMsgUDP(b, oob)
	if err == nil {
		n = c.deobfuscate(b[:n])
	}
	return
}

func (c *batchSalamanderConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.conn.ReadFrom(p)
	if err == nil {
		n = c.deobfuscate(p[:n])
	}
	return n, addr, err
}

// udpSegmentOffset 找出 oob 里 UDP_SEGMENT 的数据位置与段长；没有返回 -1。
func udpSegmentOffset(oob []byte) (int, int) {
	for off := 0; off+syscall.CmsgLen(0) <= len(oob); {
		header := (*syscall.Cmsghdr)(unsafe.Pointer(&oob[off]))
		length := int(header.Len)
		if length < syscall.CmsgLen(0) || off+length > len(oob) {
			return -1, 0
		}
		data := off + syscall.CmsgLen(0)
		if header.Level == syscall.IPPROTO_UDP && header.Type == udpSegmentOption && length >= syscall.CmsgLen(2) {
			return data, int(binary.NativeEndian.Uint16(oob[data:]))
		}
		off += syscall.CmsgSpace(length - syscall.CmsgLen(0))
	}
	return -1, 0
}

func (c *batchSalamanderConn) WriteMsgUDP(b, oob []byte, addr *net.UDPAddr) (int, int, error) {
	segment := len(b)
	offset, size := udpSegmentOffset(oob)
	if offset >= 0 && size > 0 && size < len(b) {
		segment = size
	}
	count := (len(b) + segment - 1) / segment
	pooled := c.writeBuffers.Get().(*[]byte)
	defer c.writeBuffers.Put(pooled)
	total := len(b) + count*salamanderSaltLen
	if cap(*pooled) < total {
		*pooled = make([]byte, total)
	}
	out := (*pooled)[:total]
	// 一次取够所有段的盐，避免逐段一次 getrandom。
	var salts [64 * salamanderSaltLen]byte
	saltBuf := salts[:]
	if count*salamanderSaltLen > len(saltBuf) {
		saltBuf = make([]byte, count*salamanderSaltLen)
	}
	saltBuf = saltBuf[:count*salamanderSaltLen]
	if _, err := rand.Read(saltBuf); err != nil {
		return 0, 0, err
	}
	var scratchArray [128]byte
	scratch := scratchArray[:0]
	if len(c.password)+salamanderSaltLen > len(scratchArray) {
		scratch = make([]byte, 0, len(c.password)+salamanderSaltLen)
	}
	for i, start := 0, 0; i < count; i++ {
		part := b[i*segment : min((i+1)*segment, len(b))]
		salt := out[start : start+salamanderSaltLen]
		copy(salt, saltBuf[i*salamanderSaltLen:])
		key := salamanderKey(scratch, c.password, salt)
		salamanderXOR(out[start+salamanderSaltLen:], part, &key)
		start += salamanderSaltLen + len(part)
	}
	if offset >= 0 && count > 1 {
		// 段长随盐变长：改写 UDP_SEGMENT（在 oob 的副本上改，不动 quic-go 的缓冲）。
		rewritten := append([]byte(nil), oob...)
		binary.NativeEndian.PutUint16(rewritten[offset:], uint16(segment+salamanderSaltLen))
		oob = rewritten
	}
	_, oobn, err := c.conn.WriteMsgUDP(out, oob, addr)
	if err != nil {
		return 0, oobn, err
	}
	return len(b), oobn, nil
}

func (c *batchSalamanderConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok {
		resolved, err := net.ResolveUDPAddr("udp", addr.String())
		if err != nil {
			return 0, err
		}
		udpAddr = resolved
	}
	n, _, err := c.WriteMsgUDP(p, nil, udpAddr)
	return n, err
}

func (c *batchSalamanderConn) SyscallConn() (syscall.RawConn, error) { return c.conn.SyscallConn() }
func (c *batchSalamanderConn) SetReadBuffer(bytes int) error         { return c.conn.SetReadBuffer(bytes) }
func (c *batchSalamanderConn) SetWriteBuffer(bytes int) error        { return c.conn.SetWriteBuffer(bytes) }
func (c *batchSalamanderConn) LocalAddr() net.Addr                   { return c.conn.LocalAddr() }
func (c *batchSalamanderConn) Close() error                          { return c.conn.Close() }
func (c *batchSalamanderConn) SetDeadline(t time.Time) error         { return c.conn.SetDeadline(t) }
func (c *batchSalamanderConn) SetReadDeadline(t time.Time) error     { return c.conn.SetReadDeadline(t) }
func (c *batchSalamanderConn) SetWriteDeadline(t time.Time) error    { return c.conn.SetWriteDeadline(t) }
