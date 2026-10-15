package kernel

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

// ss2022SaltFilterMaxPerGen 是 TCP salt 表每代上限。
// SIP022 要求记 60 秒；按 30 秒一代保留 3 代，至少 60 秒、至多 90 秒；
// 只记认证通过的 salt；每代满了提前换代，洪泛下缩短保留期但内存有上限，
// 最坏约 3×2^17 条、几十 MB，只在每秒 4000 多条新认证连接持续时才会到。
const ss2022SaltFilterMaxPerGen = 1 << 17

// sharedSS2022SaltFilter 是全进程 SS2022 共用的一张（多个入站同一客户端重放到别的入站也挡得住）。
var sharedSS2022SaltFilter = sync.OnceValue(func() *replayFilter {
	return newReplayFilter(30*time.Second, 3, ss2022SaltFilterMaxPerGen)
})

func (a *ss2022Adapter) acceptSalt(salt []byte) bool {
	return a.salts == nil || a.salts.check(salt, time.Now())
}

// handleConn 是 TCP 会话入口，会话层失败在这里上报。
func (a *ss2022Adapter) handleConn(ctx context.Context, conn net.Conn) error {
	err := a.serveConn(ctx, conn)
	a.connErr.conn(StageSession, conn, err)
	finishSession(conn, err)
	return err
}

// serveConn 不关 conn：由 handleConn 上报之后经 finishSession 收尾。
func (a *ss2022Adapter) serveConn(ctx context.Context, conn net.Conn) error {
	epoch := a.sessions.epoch()
	_ = conn.SetReadDeadline(time.Now().Add(requestHeaderTimeout(a.headerTimeout)))
	user, stream, destination, err := a.readRequest(conn)
	if err != nil {
		return drainAfterReport{err}
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	sess := a.sessions.open(user, epoch, conn)
	if sess == nil {
		return errSessionRevoked
	}
	defer sess.close()
	ip := remoteIP(conn.RemoteAddr())
	if !a.online.enter(user, ip) {
		return deviceLimitError("shadowsocks 2022")
	}
	defer a.online.leave(user, ip)
	sourceIP, _ := netip.ParseAddr(ip)
	var sourcePort uint16
	if _, p, e := net.SplitHostPort(conn.RemoteAddr().String()); e == nil {
		if n, e := strconv.ParseUint(p, 10, 16); e == nil {
			sourcePort = uint16(n)
		}
	}
	meta := route.Meta{
		Domain: destination.Domain, IP: destination.IP, Port: destination.Port,
		Network: "tcp", Protocol: "shadowsocks-2022", SourceIP: sourceIP, SourcePort: sourcePort,
	}
	upstream, err := a.plane.DialTCP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	sess.relay(stream, upstream, core.RelayOptions{Limiter: a.limiters.For(user)})
	return nil
}

func (a *ss2022Adapter) readRequest(conn net.Conn) (core.User, *ss2022Conn, vlessDestination, error) {
	var destination vlessDestination
	keyLen := a.method.keyLen
	head := make([]byte, keyLen+ss2022EIHLen+11+ss2022Overhead)
	if _, err := io.ReadFull(conn, head); err != nil {
		return core.User{}, nil, destination, err
	}
	salt := head[:keyLen]
	eih := head[keyLen : keyLen+ss2022EIHLen]
	fixedWire := head[keyLen+ss2022EIHLen:]

	user := a.identifyTCPUser(salt, eih)
	if user == nil {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 user authentication failed"))
	}

	readAEAD, err := newAESGCM(ss2022SessionKey(user.MasterKey, salt, keyLen))
	if err != nil {
		return core.User{}, nil, destination, err
	}
	var nonce [12]byte
	fixed, err := readAEAD.Open(fixedWire[:0], fillSSNonce(&nonce, 0), fixedWire, nil)
	if err != nil {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}
	if len(fixed) != 11 {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}
	if fixed[0] != ss2022ClientHeader {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}
	stamp := int64(binary.BigEndian.Uint64(fixed[1:9]))
	now := time.Now().Unix()
	if d := now - stamp; d > ss2022MaxTimeDiff || d < -ss2022MaxTimeDiff {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 timestamp outside 30 seconds"))
	}
	if !a.acceptSalt(salt) {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 salt replayed"))
	}

	varLen := int(binary.BigEndian.Uint16(fixed[9:11]))
	if varLen < 9 {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}
	varWire := make([]byte, varLen+ss2022Overhead)
	if _, err := io.ReadFull(conn, varWire); err != nil {
		return core.User{}, nil, destination, err
	}
	plain, err := readAEAD.Open(varWire[:0], fillSSNonce(&nonce, 1), varWire, nil)
	if err != nil {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}

	consumed, err := parseSSDestination(plain, &destination)
	if err != nil {
		return core.User{}, nil, destination, err
	}
	if consumed+2 > len(plain) {
		return core.User{}, nil, destination, io.ErrUnexpectedEOF
	}
	padLen := int(binary.BigEndian.Uint16(plain[consumed:]))
	if consumed+2+padLen > len(plain) {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request header authentication failed"))
	}
	payload := plain[consumed+2+padLen:]
	if padLen == 0 && len(payload) == 0 {
		return core.User{}, nil, destination, markConnError(connErrAuth, errors.New("shadowsocks 2022 request has neither padding nor payload"))
	}

	respSalt := make([]byte, keyLen)
	if _, err := rand.Read(respSalt); err != nil {
		return core.User{}, nil, destination, err
	}
	writeAEAD, err := newAESGCM(ss2022SessionKey(user.MasterKey, respSalt, keyLen))
	if err != nil {
		return core.User{}, nil, destination, err
	}

	stream := &ss2022Conn{
		conn: conn, readAEAD: readAEAD, readNonce: 2,
		writeAEAD: writeAEAD, keyLen: keyLen,
		respSalt: respSalt, reqSalt: salt, pending: payload,
	}
	cu := core.User{ID: user.ID, DeviceLimit: user.DeviceLimit, SpeedLimit: user.SpeedLimit}
	return cu, stream, destination, nil
}

func (a *ss2022Adapter) identifyTCPUser(salt, eih []byte) *ssUser {
	keyLen := a.method.keyLen
	idKey := ss2022IdentitySubkey(a.ipsk, salt, keyLen)
	block, err := aes.NewCipher(idKey)
	if err != nil {
		return nil
	}
	var hash [16]byte
	block.Decrypt(hash[:], eih)
	return a.lookupUser(hash)
}
