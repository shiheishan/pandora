package kernel

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var (
	errSSUDPSourceInvalid   = errors.New("shadowsocks UDP response source invalid")
	errSSUDPPayloadTooLarge = errors.New("shadowsocks UDP payload too large")
)

// ssUDPMaxAddrLen 是域名地址头的上限。
const ssUDPMaxAddrLen = 1 + 1 + 255 + 2

// ssUDPEncodeHeadroom 是地址头之前最多要留的字节：经典 salt ≤32；SS2022 分离头 16 + 类型 1 + 时间戳 8 + 客户端 session id 8 + 填充长度 2 = 35。
const ssUDPEncodeHeadroom = 35

// ssUDPEncodeBufSize 是编一个下行包要的最大缓冲：头部 + 地址头（域名最长 259）+ 负载 + 标签 16。
const ssUDPEncodeBufSize = ssUDPEncodeHeadroom + ssUDPMaxAddrLen + ssUDPMaxPacket + 16

// ssUDPEncodePool 是下行编码缓冲：只在 WritePacket 期间借用，空闲会话不常驻。
var ssUDPEncodePool = sync.Pool{New: func() any {
	b := make([]byte, ssUDPEncodeBufSize)
	return &b
}}

// putSSUDPSource 把回包的来源地址按 SOCKS 地址格式写进 dst 开头，返回写了几个字节。
// IP 按 ATYP 1 / 4（先 Unmap），域名按 ATYP 3（长度 1–255）；端口为 0 或地址无效返回 errSSUDPSourceInvalid。
func putSSUDPSource(dst []byte, source M.Socksaddr) (int, error) {
	if source.Port == 0 {
		return 0, errSSUDPSourceInvalid
	}
	n := 0
	if source.IsIP() {
		addr := source.Addr.Unmap()
		if !addr.IsValid() {
			return 0, errSSUDPSourceInvalid
		}
		if addr.Is4() {
			dst[0] = 1
			ip4 := addr.As4()
			n = 1 + copy(dst[1:], ip4[:])
		} else {
			dst[0] = 4
			ip16 := addr.As16()
			n = 1 + copy(dst[1:], ip16[:])
		}
	} else if source.IsDomain() {
		fqdn := source.Fqdn
		if len(fqdn) < 1 || len(fqdn) > 255 {
			return 0, errSSUDPSourceInvalid
		}
		dst[0] = 3
		dst[1] = byte(len(fqdn))
		n = 2 + copy(dst[2:], fqdn)
	} else {
		return 0, errSSUDPSourceInvalid
	}
	binary.BigEndian.PutUint16(dst[n:], source.Port)
	return n + 2, nil
}

// encodeUDPPacket 把上游回包编成 SS UDP 包（salt + AEAD(来源地址 + 负载)），写进 dst
// （容量至少 ssUDPEncodeBufSize，调用方从 ssUDPEncodePool 借），返回 dst 的前缀。
// 回包地址写上游的真实来源：IP 按 ATYP 1 / 4，中转出站（如 socks5）给的域名来源按 ATYP 3。
func (a *shadowsocksAdapter) encodeUDPPacket(dst, master []byte, source M.Socksaddr, payload []byte) ([]byte, error) {
	if len(payload) > ssUDPMaxPacket {
		return nil, errSSUDPPayloadTooLarge
	}
	saltLen := a.method.SaltLen
	salt := dst[:saltLen]
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	sc := getSSScratch()
	sc.setSalt(salt)
	aead, err := a.method.NewAEAD(sc.subkey(master, a.method.KeyLen))
	putSSScratch(sc)
	if err != nil {
		return nil, err
	}
	plain := dst[saltLen:]
	addrLen, err := putSSUDPSource(plain, source)
	if err != nil {
		return nil, err
	}
	n := addrLen + copy(plain[addrLen:], payload)
	// 每个 UDP 包的 salt 都是新的、子密钥只用一次，nonce 用全零（SIP004）。
	var nonce [12]byte
	sealed := aead.Seal(plain[:0], nonce[:], plain[:n], nil)
	return dst[:saltLen+len(sealed)], nil
}

// encodeUDPSession 是经典 SS 的 udpEncode：用会话用户的主密钥编下行包。
func (a *shadowsocksAdapter) encodeUDPSession(s *ssUDPSession, dst []byte, source M.Socksaddr, payload []byte) ([]byte, error) {
	return a.encodeUDPPacket(dst, s.user.MasterKey, source, payload)
}

// ssUDPClientWriter 是交给 hy2DownlinkUDP 的「客户端连接」：WritePacket 把上游回包按 SS UDP 格式
// 加密（salt + AEAD(地址 + 负载)），写回入站 UDP socket 上的客户端地址。只用到 WritePacket。
type ssUDPClientWriter struct{ s *ssUDPSession }

var _ N.PacketConn = ssUDPClientWriter{}

func (w ssUDPClientWriter) WritePacket(buffer *buf.Buffer, source M.Socksaddr) error {
	defer buffer.Release()
	s := w.s
	payload := buffer.Bytes()
	if s.limiter != nil {
		if err := s.limiter.WaitN(s.ctx, len(payload)); err != nil {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			s.hub.udpDrops.downlink.Add(1) // 超过桶容量等：只丢这个包
			return nil
		}
	}
	bp := ssUDPEncodePool.Get().(*[]byte)
	out, err := s.hub.udpEncode(s, *bp, source, payload)
	if err == nil {
		_, err = s.hub.udpConn.WriteToUDPAddrPort(out, s.clientAddr())
	}
	ssUDPEncodePool.Put(bp)
	if err != nil {
		// 入站 socket 关了或会话已取消才结束会话；编码失败、ENOBUFS 之类的临时写错误只丢这个包。
		if errors.Is(err, net.ErrClosed) || s.ctx.Err() != nil {
			return err
		}
		s.hub.udpDrops.downlink.Add(1)
		return nil
	}
	return nil
}

func (w ssUDPClientWriter) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, os.ErrInvalid
}

func (w ssUDPClientWriter) Close() error {
	w.s.cancel()
	return nil
}

func (w ssUDPClientWriter) LocalAddr() net.Addr {
	return w.s.hub.udpConn.LocalAddr()
}

func (w ssUDPClientWriter) SetDeadline(t time.Time) error      { return nil }
func (w ssUDPClientWriter) SetReadDeadline(t time.Time) error  { return nil }
func (w ssUDPClientWriter) SetWriteDeadline(t time.Time) error { return nil }
