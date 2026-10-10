package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	quic "github.com/apernet/quic-go"
	M "github.com/sagernet/sing/common/metadata"
)

// Juicity 的 UDP 转发：客户端每个 UDP 关联一条双向流，流上每个包自带目标；
// 服务端按目标分路由，每条路由一个上游 socket 和一个下行读 goroutine。
//
// 热路径不分配：
//   - 上行逐包读进本流独占的缓冲（juicityPacketReader），路由表直接按原始地址
//     编码查（域名先就地转小写）；只有新目标才解析地址、建路由；
//   - 目标解析按路由缓存：IP 目标只解析一次，域名目标 30 秒（与 hy2 同口径）；
//     以前每个包都 net.ResolveUDPAddr 一次，域名目标就是每包一次 DNS 查询；
//   - 下行读进预留了帧头空间的缓冲，来源地址的编码按上一包缓存，帧头直接写在
//     负载前面，整帧一次 Write。
//
// 每用户路由数受 udpQuota 约束（quic_udp_quota.go，与 hy2 / TUIC 的 UDP 会话同一
// 上限）：超出的新目标丢包并上报 limit，已有路由不受影响。

const (
	// juicityUDPResolveTTL 是域名目标解析结果在路由内的复用时长。
	juicityUDPResolveTTL = hy2UDPResolveTTL
	// juicityMaxAddrLen 是地址编码的最大长度：类型 1 + 长度 1 + 域名 253 + 端口 2。
	juicityMaxAddrLen = 1 + 1 + 253 + 2
	// juicityFrameHeadroom 是下行帧在负载前预留的空间：地址编码加 2 字节长度。
	juicityFrameHeadroom = juicityMaxAddrLen + 2
)

// juicityResolveUDP 解析路由目标；测试替换它来数解析次数。
var juicityResolveUDP = resolveUDPAddr

// juicityUDPRoute 是一条 UDP 流里发往同一目标的路由。addr / resolved 只由该流的
// 上行 goroutine 读写；下行 goroutine 只用 pc 与 target。
type juicityUDPRoute struct {
	pc       net.PacketConn
	target   juicityAddress
	dest     M.Socksaddr
	addr     *net.UDPAddr
	resolved time.Time
}

func (r *juicityUDPRoute) resolve(ctx context.Context) (*net.UDPAddr, error) {
	if r.addr != nil && (r.dest.IsIP() || time.Since(r.resolved) < juicityUDPResolveTTL) {
		return r.addr, nil
	}
	addr, err := juicityResolveUDP(ctx, r.dest)
	if err != nil {
		return nil, err
	}
	r.addr, r.resolved = addr, time.Now()
	return addr, nil
}

func (a *juicityAdapter) handleUDPStream(ctx context.Context, conn *quic.Conn, stream *quic.Stream, user core.User, ip string, initial juicityAddress) {
	sourceIP, sourcePort := sourceFromAddr(conn.RemoteAddr())
	baseMeta := route.Meta{Network: "udp", Protocol: "juicity", SourceIP: sourceIP, SourcePort: sourcePort}
	// routes 只在本 goroutine 里读写（下行 goroutine 拿的是各自的路由指针）。
	routes := make(map[string]*juicityUDPRoute)
	var writeMu sync.Mutex
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		for _, r := range routes {
			_ = r.pc.Close()
			a.udpQuota.release(user.ID)
		}
	}()
	// The first address is the stream's advertised default target. Actual
	// packets carry their own destination metadata, as required by Juicity.
	_ = initial
	reader := &juicityPacketReader{r: stream}
	for {
		rawAddr, payload, err := reader.next()
		if err != nil {
			return
		}
		r := routes[string(rawAddr)]
		if r == nil {
			target, err := readJuicityAddress(bytes.NewReader(rawAddr))
			if err != nil {
				return
			}
			if !a.udpQuota.acquire(user.ID) {
				a.connErr.addr(StageSession, conn.RemoteAddr(), udpSessionLimitError("juicity"))
				continue
			}
			meta := baseMeta
			meta.Domain, meta.IP, meta.Port = target.domain(), target.ip(), target.Port
			dest := M.ParseSocksaddrHostPort(target.Host, target.Port)
			pc, openErr := a.plane.ListenUDP(streamCtx, meta, dest)
			if openErr != nil {
				a.udpQuota.release(user.ID)
				a.connErr.addr(StageSession, conn.RemoteAddr(), openErr)
				return
			}
			r = &juicityUDPRoute{pc: pc, target: target, dest: dest}
			routes[string(rawAddr)] = r
			go a.juicityUDPReader(streamCtx, stream, &writeMu, r, user)
		}
		addr, err := r.resolve(streamCtx)
		if err != nil {
			return
		}
		n, err := r.pc.WriteTo(payload, addr)
		if err != nil {
			return
		}
		a.addTraffic(user, int64(n), 0)
	}
}

// udpAddrPortReader 是 *net.UDPConn 的不分配读法（ReadFrom 每包新建一个 *UDPAddr）。
type udpAddrPortReader interface {
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
}

func (a *juicityAdapter) juicityUDPReader(ctx context.Context, stream *quic.Stream, writeMu *sync.Mutex, r *juicityUDPRoute, user core.User) {
	framer := newJuicityDownlinkFramer(r.target)
	fast, _ := r.pc.(udpAddrPortReader)
	for {
		var n int
		var source netip.AddrPort
		var other net.Addr
		var err error
		if fast != nil {
			n, source, err = fast.ReadFromUDPAddrPort(framer.payload())
		} else {
			n, other, err = r.pc.ReadFrom(framer.payload())
			if udp, ok := other.(*net.UDPAddr); ok {
				source = udp.AddrPort()
			}
		}
		if err != nil {
			return
		}
		frame, n := framer.frame(n, source, other)
		writeMu.Lock()
		_, writeErr := stream.Write(frame)
		writeMu.Unlock()
		if writeErr != nil {
			return
		}
		a.addTraffic(user, 0, int64(n))
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// juicityDownlinkFramer 把上游收到的包就地组成下行帧：负载读进预留了帧头空间的
// 缓冲（payload），帧头（来源地址编码与 2 字节长度）倒着写在负载前面，整帧一次
// Write。来源地址的编码按上一包缓存，同一来源连续的包不分配。
type juicityDownlinkFramer struct {
	buf    []byte
	target juicityAddress
	last   netip.AddrPort
	header []byte
}

func newJuicityDownlinkFramer(target juicityAddress) *juicityDownlinkFramer {
	// 64KB 与 hy2 非批量下行同大：上游 UDP 包可能分片重组得很大，缓冲小了会截断。
	return &juicityDownlinkFramer{buf: make([]byte, juicityFrameHeadroom+juicityMaxPacket), target: target}
}

// payload 是下一包要读进的位置。
func (f *juicityDownlinkFramer) payload() []byte { return f.buf[juicityFrameHeadroom:] }

// frame 返回刚读进 payload 的 n 字节组成的整帧，以及实际计入的负载长度。
// source 无效时（出站给的不是 UDP 地址，少见）按 other 的字符串解析，解析不了就
// 用路由目标。
func (f *juicityDownlinkFramer) frame(n int, source netip.AddrPort, other net.Addr) ([]byte, int) {
	if n > 65535 {
		n = 65535
	}
	source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
	if source.IsValid() && source.Port() != 0 {
		if f.header == nil || source != f.last {
			f.header, f.last = marshalJuicityAddrPort(source), source
		}
	} else {
		response, ok := juicityAddressFromNetAddr(other)
		if !ok {
			response = f.target
		}
		f.header, f.last = marshalJuicityAddress(response), netip.AddrPort{}
	}
	start := juicityFrameHeadroom - 2 - len(f.header)
	copy(f.buf[start:], f.header)
	binary.BigEndian.PutUint16(f.buf[juicityFrameHeadroom-2:], uint16(n))
	return f.buf[start : juicityFrameHeadroom+n], n
}

// marshalJuicityAddrPort 按 Juicity 的地址格式编码 IP 来源（IPv4 类型 1，IPv6 类型 4）。
func marshalJuicityAddrPort(source netip.AddrPort) []byte {
	ip := source.Addr()
	if ip.Is4() {
		out := make([]byte, 7)
		out[0] = 1
		v4 := ip.As4()
		copy(out[1:5], v4[:])
		binary.BigEndian.PutUint16(out[5:], source.Port())
		return out
	}
	out := make([]byte, 19)
	out[0] = 4
	v6 := ip.As16()
	copy(out[1:17], v6[:])
	binary.BigEndian.PutUint16(out[17:], source.Port())
	return out
}

// juicityPacketReader 逐包读上行 UDP 帧（地址、2 字节长度、负载）。缓冲随结构体
// 一次分配、本流独占，返回的切片在下一次 next 之前有效。
type juicityPacketReader struct {
	r       io.Reader
	addr    [juicityMaxAddrLen]byte
	length  [2]byte
	payload [juicityMaxPacket]byte
}

// next 返回原始地址编码（域名已就地转成小写，作路由表键）与负载。
func (p *juicityPacketReader) next() ([]byte, []byte, error) {
	if _, err := io.ReadFull(p.r, p.addr[:1]); err != nil {
		return nil, nil, err
	}
	var n int
	switch p.addr[0] {
	case 1:
		n = 1 + 4 + 2
	case 4:
		n = 1 + 16 + 2
	case 3:
		if _, err := io.ReadFull(p.r, p.addr[1:2]); err != nil {
			return nil, nil, err
		}
		length := int(p.addr[1])
		if length == 0 || length > 253 {
			return nil, nil, fmt.Errorf("juicity domain length is invalid")
		}
		n = 2 + length + 2
		if _, err := io.ReadFull(p.r, p.addr[2:n]); err != nil {
			return nil, nil, err
		}
		asciiLower(p.addr[2 : 2+length])
	default:
		return nil, nil, fmt.Errorf("juicity address type %d is unsupported", p.addr[0])
	}
	if p.addr[0] != 3 {
		if _, err := io.ReadFull(p.r, p.addr[1:n]); err != nil {
			return nil, nil, err
		}
	}
	if binary.BigEndian.Uint16(p.addr[n-2:n]) == 0 {
		return nil, nil, fmt.Errorf("juicity destination port is zero")
	}
	if _, err := io.ReadFull(p.r, p.length[:]); err != nil {
		return nil, nil, err
	}
	size := int(binary.BigEndian.Uint16(p.length[:]))
	if size == 0 || size > juicityMaxPacket {
		return nil, nil, fmt.Errorf("juicity packet length is invalid")
	}
	if _, err := io.ReadFull(p.r, p.payload[:size]); err != nil {
		return nil, nil, err
	}
	return p.addr[:n], p.payload[:size], nil
}

func asciiLower(b []byte) {
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
}
