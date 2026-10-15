package kernel

// Shadowsocks 2022（SIP022 + SIP023）多用户 UDP：入站只有一个读循环，复用经典 SS 的 ssUDPHub
// 会话表与慢路径 worker 池，不按包起 goroutine。
//
// 客户端 → 服务端：分离头 16（iPSK AES-ECB）| EIH 16（与分离头明文异或得用户 hash）| AEAD 密文。
// AEAD 密钥 = ss2022SessionKey(uPSK, 客户端 session id 8 字节)，nonce = 分离头明文 [4:16]。
// 明文：类型 0 | 时间戳 8 | 填充长度 2 | 填充 | SOCKS 目标 | 负载。
//
// 服务端 → 客户端：分离头 16（uPSK AES-ECB，无 EIH）| AEAD 密文；密钥 = ss2022SessionKey(uPSK, 服务端 session id)，
// 包号从 0 递增；明文：类型 1 | 时间戳 8 | 客户端 session id 8 | 填充 0 | 来源地址 | 负载。
//
// 每包查时间戳（±30 秒）；每会话对客户端 packet id 做滑动窗口（SIP022），重复或落在窗口之后丢弃。
// 路由按客户端 session id，不按来源地址；验证通过的包来自新地址时 roam 换回包地址（换网络会话不断）。
// 认不出用户的包在读循环快路径丢弃（不进慢路径），灌垃圾只占两次 AES 与一次 map 查找。
//
// 已知取舍：UpsertUsers 换掉用户记录时旧会话取消、新会话窗口从空开始，30 秒内重放旧会话的包可能被新会话收一次；
// 只在后台改用户时发生，不值得跨会话搬窗口。

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	ss2022sw "github.com/sagernet/sing-shadowsocks2/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	ss2022UDPSepHeader = 16 // 分离头
	// ss2022UDPMinPacket 是客户端包的最短长度：分离头 16 + 身份头 16 + 类型 1 + 时间戳 8 + 填充长度 2 + 最短地址（IPv4）7 + 标签 16。
	ss2022UDPMinPacket = 16 + 16 + 1 + 8 + 2 + 7 + 16
)

var (
	errSS2022UDPTooShort = markConnError(connErrAuth, errors.New("shadowsocks 2022 UDP packet too short"))
	errSS2022UDPAuth     = markConnError(connErrAuth, errors.New("shadowsocks 2022 UDP user authentication failed"))
	errSS2022UDPHeader   = markConnError(connErrAuth, errors.New("shadowsocks 2022 UDP header invalid"))
	errSS2022UDPStale    = markConnError(connErrAuth, errors.New("shadowsocks 2022 UDP timestamp outside 30 seconds"))
	errSS2022UDPReplay   = markConnError(connErrAuth, errors.New("shadowsocks 2022 UDP packet id replayed or outside window"))
)

// ss2022UDPState 是一个 SS2022 UDP 会话的协议状态：慢路径建会话前算好，之后除 serverPID 与 window 外只读。
type ss2022UDPState struct {
	clientSID uint64
	serverSID uint64
	remote    cipher.AEAD   // 解客户端包：ss2022SessionKey(uPSK, clientSID 大端 8 字节)
	local     cipher.AEAD   // 封下行包：ss2022SessionKey(uPSK, serverSID 大端 8 字节)
	block     cipher.Block  // aes(uPSK)：加密下行分离头
	serverPID atomic.Uint64 // 下一个下行包号（从 0 起）
	winMu     sync.Mutex    // 读循环与慢路径 worker 可能同时处理同一会话的包
	window    ss2022sw.SlidingWindow
}

// ss2022UDPScratch 是解包用的小缓冲。分组密码经接口调用，栈上数组传进去会逃逸到堆上，
// 读循环持有一个复用，快路径每包不分配；慢路径每包 new 一个。
type ss2022UDPScratch struct {
	hdr [16]byte // 分离头明文：session id + packet id
	eih [16]byte
}

// openUDPHeader 解分离头与身份头，认出用户；认不出返回 errSS2022UDPAuth。
func (a *ss2022Adapter) openUDPHeader(sc *ss2022UDPScratch, wire []byte) (*ssUser, error) {
	if len(wire) < ss2022UDPMinPacket {
		return nil, errSS2022UDPTooShort
	}
	a.ipskBlock.Decrypt(sc.hdr[:], wire[:16])
	a.ipskBlock.Decrypt(sc.eih[:], wire[16:32])
	subtle.XORBytes(sc.eih[:], sc.eih[:], sc.hdr[:])
	u := a.lookupUser(sc.eih)
	if u == nil {
		return nil, errSS2022UDPAuth
	}
	return u, nil
}

// openUDPBody 用 remote 解包体到 plain（读循环独占或池化的缓冲；GCM 失败会清零 dst，不能原地解 wire），
// 查类型与时间戳，跳过填充，解出目标与负载（负载是 plain 的切片）。
func openSS2022UDPBody(remote cipher.AEAD, hdr []byte, wire, plain []byte) (vlessDestination, []byte, error) {
	ct := wire[32:]
	if cap(plain) < len(ct) {
		return vlessDestination{}, nil, errSS2022UDPHeader
	}
	body, err := remote.Open(plain[:0], hdr[4:16], ct, nil)
	if err != nil {
		return vlessDestination{}, nil, errSS2022UDPAuth
	}
	if len(body) < 11 || body[0] != ss2022ClientHeader {
		return vlessDestination{}, nil, errSS2022UDPHeader
	}
	stamp := int64(binary.BigEndian.Uint64(body[1:9]))
	now := time.Now().Unix()
	if d := now - stamp; d > ss2022MaxTimeDiff || d < -ss2022MaxTimeDiff {
		return vlessDestination{}, nil, errSS2022UDPStale
	}
	pad := int(binary.BigEndian.Uint16(body[9:11]))
	if 11+pad > len(body) {
		return vlessDestination{}, nil, errSS2022UDPHeader
	}
	var dest vlessDestination
	consumed, err := parseSSDestination(body[11+pad:], &dest)
	if err != nil {
		return vlessDestination{}, nil, errSS2022UDPHeader
	}
	off := 11 + pad + consumed
	if off > len(body) {
		return vlessDestination{}, nil, errSS2022UDPHeader
	}
	return dest, body[off:], nil
}

// acceptUDPPacket 对已解开、属于会话 s 的包做窗口检查、换地址，再转发。
func (a *ss2022Adapter) acceptUDPPacket(s *ssUDPSession, pid uint64, from netip.AddrPort, dest vlessDestination, payload []byte) error {
	st := s.ss2022
	st.winMu.Lock()
	if !st.window.Check(pid) {
		st.winMu.Unlock()
		a.udpDrops.replay.Add(1)
		return errSS2022UDPReplay
	}
	st.window.Add(pid)
	st.winMu.Unlock()
	if from != s.clientAddr() {
		if s.ctx.Err() != nil {
			return nil
		}
		if !s.roam(from) {
			return deviceLimitError(a.udpProto)
		}
	}
	return a.forwardUDP(s, dest, payload)
}

// packetLoop 是入站唯一的 UDP 读循环（照经典 shadowsocksAdapter.packetLoop）。
func (a *ss2022Adapter) packetLoop() {
	defer a.wg.Done()
	wire := make([]byte, ssUDPMaxPacket)
	plain := make([]byte, ssUDPMaxPacket)
	sc := new(ss2022UDPScratch)
	for {
		n, from, err := a.udpConn.ReadFromUDPAddrPort(wire)
		if err != nil {
			if a.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		handled, err := a.fastPathUDP(sc, wire[:n], plain, from)
		if err != nil {
			a.connErr.addr(StageSession, net.UDPAddrFromAddrPort(from), err)
		}
		if !handled {
			a.dispatchSlowUDP(wire[:n], from)
		}
	}
}

// fastPathUDP 是读循环里的快路径：认不出用户的包直接判失败（handled=true，不进慢路径）；
// 已有 (session id, 用户) 会话的包用会话预建的 AEAD 解开并转发；没有会话返回 handled=false。
func (a *ss2022Adapter) fastPathUDP(sc *ss2022UDPScratch, wire, plain []byte, from netip.AddrPort) (bool, error) {
	user, err := a.openUDPHeader(sc, wire)
	if err != nil {
		return true, err
	}
	sid := binary.BigEndian.Uint64(sc.hdr[:8])
	s := a.findUDPSession(ssUDPKey{sid: sid}, user)
	if s == nil {
		return false, nil
	}
	dest, payload, err := openSS2022UDPBody(s.ss2022.remote, sc.hdr[:], wire, plain)
	if err != nil {
		return true, err
	}
	pid := binary.BigEndian.Uint64(sc.hdr[8:16])
	return true, a.acceptUDPPacket(s, pid, from, dest, payload)
}

// slowPathUDP 是慢路径 worker 的处理函数（a.udpWorker(a.slowPathUDP)）。
func (a *ss2022Adapter) slowPathUDP(wire []byte, from netip.AddrPort) error {
	epoch := a.sessions.epoch()
	sc := new(ss2022UDPScratch)
	user, err := a.openUDPHeader(sc, wire)
	if err != nil {
		return err
	}
	sid := binary.BigEndian.Uint64(sc.hdr[:8])
	pid := binary.BigEndian.Uint64(sc.hdr[8:16])
	key := ssUDPKey{sid: sid}
	plain := buf.Get(len(wire))
	defer func() { _ = buf.Put(plain) }()
	if s := a.findUDPSession(key, user); s != nil {
		dest, payload, err := openSS2022UDPBody(s.ss2022.remote, sc.hdr[:], wire, plain)
		if err != nil {
			return err
		}
		return a.acceptUDPPacket(s, pid, from, dest, payload)
	}
	remote, err := newAESGCM(ss2022SessionKey(user.MasterKey, sc.hdr[:8], a.method.keyLen))
	if err != nil {
		return err
	}
	dest, payload, err := openSS2022UDPBody(remote, sc.hdr[:], wire, plain)
	if err != nil {
		return err
	}
	st, err := a.newUDPState(user, sid, remote)
	if err != nil {
		return err
	}
	s, err := a.getOrOpenUDPSession(key, from, user, epoch, dest, func(s *ssUDPSession) { s.ss2022 = st })
	if err != nil {
		return err
	}
	return a.acceptUDPPacket(s, pid, from, dest, payload)
}

// newUDPState 算一个新会话的协议状态：随机服务端 session id，派生下行 AEAD，建 uPSK 的分组密码。
func (a *ss2022Adapter) newUDPState(user *ssUser, clientSID uint64, remote cipher.AEAD) (*ss2022UDPState, error) {
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	serverSID := binary.BigEndian.Uint64(idBytes[:])
	local, err := newAESGCM(ss2022SessionKey(user.MasterKey, idBytes[:], a.method.keyLen))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(user.MasterKey)
	if err != nil {
		return nil, err
	}
	return &ss2022UDPState{
		clientSID: clientSID,
		serverSID: serverSID,
		remote:    remote,
		local:     local,
		block:     block,
	}, nil
}

// encodeUDPSession 是 SS2022 的 hub 下行编码函数：把上游回包编成服务端 → 客户端包写进 dst（容量至少 ssUDPEncodeBufSize），返回 dst 的前缀。每包不分配。
func (a *ss2022Adapter) encodeUDPSession(s *ssUDPSession, dst []byte, source M.Socksaddr, payload []byte) ([]byte, error) {
	if len(payload) > ssUDPMaxPacket {
		return nil, errSSUDPPayloadTooLarge
	}
	st := s.ss2022
	hdr := dst[:16]
	binary.BigEndian.PutUint64(hdr[:8], st.serverSID)
	binary.BigEndian.PutUint64(hdr[8:16], st.serverPID.Add(1)-1)
	body := dst[16:]
	body[0] = ss2022ServerHeader
	binary.BigEndian.PutUint64(body[1:9], uint64(time.Now().Unix()))
	binary.BigEndian.PutUint64(body[9:17], st.clientSID)
	body[17], body[18] = 0, 0
	n := 19
	an, err := putSSUDPSource(body[n:], source)
	if err != nil {
		return nil, err
	}
	n += an
	n += copy(body[n:], payload)
	sealed := st.local.Seal(body[:0], hdr[4:16], body[:n], nil)
	st.block.Encrypt(hdr, hdr)
	return dst[:16+len(sealed)], nil
}
