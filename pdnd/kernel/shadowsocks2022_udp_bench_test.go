package kernel

// SS2022 UDP 测试用客户端包编码与快路径/下行编码基准（每包零分配守卫）。

import (
	"crypto/aes"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"lukechampine.com/blake3"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
)

// ss2022TestClientPacket 按 SIP022/SIP023 编一个客户端 → 服务端 UDP 包（AES 方法）。
func ss2022TestClientPacket(ipsk, upsk []byte, sid, pid uint64, stamp time.Time, dest netip.AddrPort, payload []byte) []byte {
	block, err := aes.NewCipher(ipsk)
	if err != nil {
		panic(err)
	}
	var hdr [16]byte
	binary.BigEndian.PutUint64(hdr[:8], sid)
	binary.BigEndian.PutUint64(hdr[8:16], pid)
	wireHdr := make([]byte, 16)
	block.Encrypt(wireHdr, hdr[:])
	sum := blake3.Sum256(upsk)
	var userHash [16]byte
	copy(userHash[:], sum[:16])
	var eihPlain [16]byte
	for i := 0; i < 16; i++ {
		eihPlain[i] = userHash[i] ^ hdr[i]
	}
	eihWire := make([]byte, 16)
	block.Encrypt(eihWire, eihPlain[:])
	var sidBytes [8]byte
	binary.BigEndian.PutUint64(sidBytes[:], sid)
	sessionKey := ss2022SessionKey(upsk, sidBytes[:], len(upsk))
	remote, err := newAESGCM(sessionKey)
	if err != nil {
		panic(err)
	}
	addr := dest.Addr().Unmap()
	if !addr.Is4() {
		panic("ss2022TestClientPacket: 仅支持 IPv4 目标")
	}
	ip4 := addr.As4()
	plainLen := 1 + 8 + 2 + 1 + 4 + 2 + len(payload)
	plain := make([]byte, plainLen)
	plain[0] = ss2022ClientHeader
	binary.BigEndian.PutUint64(plain[1:9], uint64(stamp.Unix()))
	plain[9], plain[10] = 0, 0
	plain[11] = 1
	copy(plain[12:16], ip4[:])
	binary.BigEndian.PutUint16(plain[16:18], dest.Port())
	copy(plain[18:], payload)
	ct := remote.Seal(nil, hdr[4:16], plain, nil)
	out := make([]byte, 16+16+len(ct))
	copy(out, wireHdr)
	copy(out[16:], eihWire)
	copy(out[32:], ct)
	return out
}

func ss2022BenchUser(t testing.TB, a *ss2022Adapter, uuid string) *ssUser {
	hash := ss2022UserHash(ss2022UserPSK(uuid, a.method.keyLen))
	u := a.lookupUser(hash)
	if u == nil {
		t.Fatal("bench user not in adapter")
	}
	return u
}

func TestSS2022TestClientPacketAccepted(t *testing.T) {
	user := core.User{ID: 8901, UUID: "ss2022-udp-encoder"}
	a := startSS2022Adapter(t, "2022-blake3-aes-128-gcm", &udpRelayTestPlane{}, nil, user)
	u := ss2022BenchUser(t, a, user.UUID)
	sink, recvCount := startSSCountingSink(t)
	sinkAP := sink.AddrPort()
	ipsk := a.ipsk
	upsk := u.MasterKey
	sid, pid := uint64(0x1122334455667788), uint64(1)
	pkt := ss2022TestClientPacket(ipsk, upsk, sid, pid, time.Now(), sinkAP, []byte("encoder-probe"))
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: a.spec.Config.Port})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	if _, err := raw.Write(pkt); err != nil {
		t.Fatal(err)
	}
	if ok, why := waitFor(2*time.Second, func() (bool, string) {
		return recvCount() == 1, fmt.Sprintf("recv=%d", recvCount())
	}); !ok {
		t.Fatalf("编码器包未被服务端接受：%s", why)
	}
}

func benchSS2022UDPReadySession(b *testing.B) (*ss2022Adapter, *ssUser, uint64, netip.AddrPort, []byte, *ss2022UDPScratch, []byte) {
	user := core.User{ID: 8902, UUID: "ss2022-udp-bench"}
	a := startSS2022Adapter(b, "2022-blake3-aes-128-gcm", &udpRelayTestPlane{}, nil, user)
	u := ss2022BenchUser(b, a, user.UUID)
	sink, _ := startSSCountingSink(b)
	sinkAP := sink.AddrPort()
	from := netip.MustParseAddrPort("127.0.0.1:54321")
	sid := uint64(0xdeadbeefcafebabe)
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}
	pkt0 := ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, 1, time.Now(), sinkAP, payload)
	if err := a.slowPathUDP(pkt0, from); err != nil {
		b.Fatal(err)
	}
	key := ssUDPKey{sid: sid}
	if ok, why := waitFor(3*time.Second, func() (bool, string) {
		s := a.findUDPSession(key, u)
		if s == nil {
			return false, "no session"
		}
		return s.ready.Load(), "not ready"
	}); !ok {
		b.Fatalf("建会话：%s", why)
	}
	sc := new(ss2022UDPScratch)
	plain := make([]byte, ssUDPMaxPacket)
	return a, u, sid, from, payload, sc, plain
}

// BenchmarkSS2022UDPFastPath：已有会话时读循环快路径解包并转发（不含慢路径建会话）。
func BenchmarkSS2022UDPFastPath(b *testing.B) {
	a, u, sid, from, payload, sc, plain := benchSS2022UDPReadySession(b)
	sink, _ := startSSCountingSink(b)
	sinkAP := sink.AddrPort()
	pkts := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		pkts[i] = ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, uint64(i+2), time.Now(), sinkAP, payload)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		handled, err := a.fastPathUDP(sc, pkts[i], plain, from)
		if !handled || err != nil {
			b.Fatalf("fastPathUDP: handled=%v err=%v", handled, err)
		}
	}
}

// BenchmarkSS2022UDPOpenOnly：已有会话上下文下仅解分离头/身份头与 AEAD 包体，不转发。
func BenchmarkSS2022UDPOpenOnly(b *testing.B) {
	a, u, sid, from, payload, sc, plain := benchSS2022UDPReadySession(b)
	sink, _ := startSSCountingSink(b)
	sinkAP := sink.AddrPort()
	pkts := make([][]byte, b.N)
	for i := 0; i < b.N; i++ {
		pkts[i] = ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, uint64(i+2), time.Now(), sinkAP, payload)
	}
	s := a.findUDPSession(ssUDPKey{sid: sid}, u)
	if s == nil || s.ss2022 == nil {
		b.Fatal("session missing")
	}
	remote := s.ss2022.remote
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := a.openUDPHeader(sc, pkts[i]); err != nil {
			b.Fatal(err)
		}
		if _, _, err := openSS2022UDPBody(remote, sc.hdr[:], pkts[i], plain); err != nil {
			b.Fatal(err)
		}
	}
	_ = from
}

// BenchmarkSS2022UDPEncode：下行 encodeUDPSession 每包从池借缓冲编码，守每包零分配。
func BenchmarkSS2022UDPEncode(b *testing.B) {
	a, u, sid, _, payload, _, _ := benchSS2022UDPReadySession(b)
	s := a.findUDPSession(ssUDPKey{sid: sid}, u)
	if s == nil {
		b.Fatal("session missing")
	}
	source := M.ParseSocksaddrHostPort("127.0.0.1", 5353)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bp := ssUDPEncodePool.Get().(*[]byte)
		_, err := a.encodeUDPSession(s, *bp, source, payload)
		ssUDPEncodePool.Put(bp)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestSS2022UDPOpenAndEncodeZeroAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("race 检测器会抬高分配计数")
	}
	a, u, sid, from, payload, sc, plain := benchSS2022UDPReadySession(t)
	sink, _ := startSSCountingSink(t)
	sinkAP := sink.AddrPort()
	pkt := ss2022TestClientPacket(a.ipsk, u.MasterKey, sid, 99, time.Now(), sinkAP, payload)
	s := a.findUDPSession(ssUDPKey{sid: sid}, u)
	if s == nil || s.ss2022 == nil {
		t.Fatal("session missing")
	}
	remote := s.ss2022.remote
	openAllocs := testing.AllocsPerRun(200, func() {
		if _, err := a.openUDPHeader(sc, pkt); err != nil {
			panic(err)
		}
		if _, _, err := openSS2022UDPBody(remote, sc.hdr[:], pkt, plain); err != nil {
			panic(err)
		}
	})
	if openAllocs != 0 {
		t.Fatalf("解包 AllocsPerRun=%v，期望 0", openAllocs)
	}
	source := M.ParseSocksaddrHostPort("127.0.0.1", 5353)
	encodeAllocs := testing.AllocsPerRun(200, func() {
		bp := ssUDPEncodePool.Get().(*[]byte)
		_, err := a.encodeUDPSession(s, *bp, source, payload)
		ssUDPEncodePool.Put(bp)
		if err != nil {
			panic(err)
		}
	})
	if encodeAllocs != 0 {
		t.Fatalf("编码 AllocsPerRun=%v，期望 0", encodeAllocs)
	}
	_ = from
}
