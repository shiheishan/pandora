// [INPUT]: 依赖 shadowsocks2022.go 的 ss2022Adapter、ss2022UDPSession 与密钥派生，依赖 DataPlane 的 UDP 路由
// [OUTPUT]: 包内提供 udpSessionGC、cleanupUDPSessions、udpLoop、handleUDPPacket、encodeSS2022UDPPacket
// [POS]: kernel 的 Shadowsocks 2022 UDP：从 shadowsocks2022.go 拆出。按客户端会话 ID 维护会话并定期回收，逐包解密校验后经 DataPlane 路由并计量，回包按会话加密
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/aegispanel/nodeagent/route"
)

func (a *ss2022Adapter) udpSessionGC() {
	defer a.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.cleanupUDPSessions(time.Now())
		case <-a.ctx.Done():
			return
		}
	}
}

func (a *ss2022Adapter) cleanupUDPSessions(now time.Time) {
	a.udpMu.Lock()
	for key, session := range a.udp {
		if !session.lastSeen.IsZero() && now.Sub(session.lastSeen) > ss2022UDPIdleTimeout {
			delete(a.udp, key)
		}
	}
	a.udpMu.Unlock()
}

func (a *ss2022Adapter) udpLoop() {
	defer a.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, addr, err := a.packet.ReadFrom(buffer)
		if err != nil {
			a.mu.RLock()
			closed := a.closed
			a.mu.RUnlock()
			if closed || a.ctx.Err() != nil {
				return
			}
			continue
		}
		wire := append([]byte(nil), buffer[:n]...)
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			_ = a.handleUDPPacket(a.ctx, wire, addr)
		}()
	}
}

func (a *ss2022Adapter) handleUDPPacket(ctx context.Context, wire []byte, clientAddr net.Addr) error {
	chachaUDP := strings.Contains(a.method.name, "chacha20")
	if len(wire) < aes.BlockSize+ss2022Overhead && !chachaUDP || chachaUDP && len(wire) < chacha20poly1305.NonceSizeX+16+ss2022Overhead {
		return fmt.Errorf("shadowsocks 2022 UDP packet too short")
	}
	var clientID, packetID uint64
	var remote cipher.AEAD
	var plain []byte
	var packetPSK []byte
	var authErr error
	for _, candidate := range a.psks {
		if chachaUDP {
			udpCipher, err := chacha20poly1305.NewX(candidate)
			if err != nil {
				authErr = err
				continue
			}
			candidatePlain, err := udpCipher.Open(nil, wire[:chacha20poly1305.NonceSizeX], wire[chacha20poly1305.NonceSizeX:], nil)
			if err != nil {
				authErr = err
				continue
			}
			if len(candidatePlain) < 16 {
				authErr = fmt.Errorf("shadowsocks 2022 UDP packet session header missing")
				continue
			}
			clientID = binary.BigEndian.Uint64(candidatePlain[:8])
			packetID = binary.BigEndian.Uint64(candidatePlain[8:16])
			plain = candidatePlain[16:]
			packetPSK = candidate
			break
		}
		block, err := aes.NewCipher(candidate)
		if err != nil {
			authErr = err
			continue
		}
		packetHeader := append([]byte(nil), wire[:aes.BlockSize]...)
		block.Decrypt(packetHeader, packetHeader)
		candidateRemote, err := a.method.newAEAD(ss2022SessionKey(candidate, packetHeader[:8], a.method.keyLen))
		if err != nil {
			authErr = err
			continue
		}
		candidatePlain, err := candidateRemote.Open(nil, packetHeader[4:16], wire[aes.BlockSize:], nil)
		if err != nil {
			authErr = err
			continue
		}
		clientID = binary.BigEndian.Uint64(packetHeader[:8])
		packetID = binary.BigEndian.Uint64(packetHeader[8:16])
		plain = candidatePlain
		remote = candidateRemote
		packetPSK = candidate
		break
	}
	if packetPSK == nil {
		if authErr == nil {
			authErr = fmt.Errorf("no configured PSK")
		}
		return fmt.Errorf("shadowsocks 2022 UDP packet authentication failed: %w", authErr)
	}
	if len(plain) < 11 || plain[0] != ss2022ClientHeader {
		return fmt.Errorf("shadowsocks 2022 UDP header invalid")
	}
	stamp := int64(binary.BigEndian.Uint64(plain[1:9]))
	if delta := time.Now().Unix() - stamp; delta > 30 || delta < -30 {
		return fmt.Errorf("shadowsocks 2022 UDP timestamp outside 30 seconds")
	}
	paddingLen := int(binary.BigEndian.Uint16(plain[9:11]))
	if paddingLen < 0 || 11+paddingLen >= len(plain) {
		return fmt.Errorf("shadowsocks 2022 UDP padding invalid")
	}
	var destination vlessDestination
	consumed, err := parseSSDestination(plain[11+paddingLen:], &destination)
	if err != nil {
		return err
	}
	payloadOffset := 11 + paddingLen + consumed
	if payloadOffset > len(plain) {
		return io.ErrUnexpectedEOF
	}
	payload := plain[payloadOffset:]
	ip := remoteIP(clientAddr)
	a.mu.RLock()
	user, hasUser := a.user, a.hasUser
	a.mu.RUnlock()
	if !hasUser {
		return fmt.Errorf("shadowsocks 2022 has no configured user")
	}
	if !a.enterDevice(user, ip) {
		return fmt.Errorf("shadowsocks 2022 device limit")
	}
	defer a.leaveDevice(user, ip)
	sessionKey := clientAddr.String() + "#" + strconv.FormatUint(clientID, 10)
	a.udpMu.Lock()
	session := a.udp[sessionKey]
	if session == nil {
		var idBytes [8]byte
		if _, err := io.ReadFull(rand.Reader, idBytes[:]); err != nil {
			a.udpMu.Unlock()
			return err
		}
		serverID := binary.BigEndian.Uint64(idBytes[:])
		var local cipher.AEAD
		if !chachaUDP {
			localKey := make([]byte, a.method.keyLen)
			binary.BigEndian.PutUint64(localKey[:8], serverID)
			local, err = a.method.newAEAD(ss2022SessionKey(a.psk, localKey[:8], a.method.keyLen))
			if err != nil {
				a.udpMu.Unlock()
				return err
			}
		}
		session = &ss2022UDPSession{psk: append([]byte(nil), packetPSK...), clientID: clientID, serverID: serverID, remote: remote, local: local, user: user, lastSeen: time.Now()}
		a.udp[sessionKey] = session
	} else if !bytes.Equal(session.psk, packetPSK) {
		a.udpMu.Unlock()
		return fmt.Errorf("shadowsocks 2022 UDP PSK changed for session")
	}
	if session.clientSeen && packetID <= session.lastClientID {
		a.udpMu.Unlock()
		return fmt.Errorf("shadowsocks 2022 UDP packet replay")
	}
	session.lastClientID = packetID
	session.clientSeen = true
	session.lastSeen = time.Now()
	a.udpMu.Unlock()
	sourceIP, _ := netip.ParseAddr(ip)
	meta := route.Meta{Domain: destination.Domain, IP: destination.IP, Port: destination.Port, Network: "udp", Protocol: "shadowsocks-2022", SourceIP: sourceIP}
	upstream, err := a.plane.ListenUDP(ctx, meta, M.ParseSocksaddrHostPort(destination.Host, destination.Port))
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := destinationUDPAddr(destination)
	if err != nil {
		return err
	}
	if _, err := upstream.WriteTo(payload, destinationAddr); err != nil {
		return err
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, 64<<10)
	n, sourceAddr, err := upstream.ReadFrom(response)
	if err != nil {
		return err
	}
	responseDestination := destination
	if sourceAddr != nil {
		responseDestination = destinationFromNetAddr(sourceAddr)
	}
	encoded, err := a.encodeSS2022UDPPacket(session, responseDestination, response[:n])
	if err != nil {
		return err
	}
	if _, err := a.packet.WriteTo(encoded, clientAddr); err != nil {
		return err
	}
	a.addTraffic(user, int64(len(payload)), int64(n))
	return nil
}

func (a *ss2022Adapter) encodeSS2022UDPPacket(session *ss2022UDPSession, destination vlessDestination, payload []byte) ([]byte, error) {
	address, err := serializeSSDestination(destination)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, 0, 19+len(address)+len(payload))
	plain = append(plain, ss2022ServerHeader)
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(time.Now().Unix()))
	plain = append(plain, stamp[:]...)
	var remoteID [8]byte
	binary.BigEndian.PutUint64(remoteID[:], session.clientID)
	plain = append(plain, remoteID[:]...)
	plain = append(plain, make([]byte, 2)...)
	plain = append(plain, address...)
	plain = append(plain, payload...)
	if strings.Contains(a.method.name, "chacha20") {
		udpCipher, err := chacha20poly1305.NewX(session.psk)
		if err != nil {
			return nil, err
		}
		var sessionHeader [16]byte
		binary.BigEndian.PutUint64(sessionHeader[:8], session.clientID)
		a.udpMu.Lock()
		responseID := session.nextServerID
		session.nextServerID++
		a.udpMu.Unlock()
		binary.BigEndian.PutUint64(sessionHeader[8:], responseID)
		body := append(sessionHeader[:0:0], sessionHeader[:]...)
		body = append(body, plain...)
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return nil, err
		}
		return append(nonce, udpCipher.Seal(nil, nonce, body, nil)...), nil
	}
	var header [16]byte
	binary.BigEndian.PutUint64(header[:8], session.serverID)
	a.udpMu.Lock()
	responseID := session.nextServerID
	session.nextServerID++
	a.udpMu.Unlock()
	binary.BigEndian.PutUint64(header[8:], responseID)
	ciphertext := session.local.Seal(nil, header[4:16], plain, nil)
	block, err := aes.NewCipher(session.psk)
	if err != nil {
		return nil, err
	}
	block.Encrypt(header[:], header[:])
	return append(header[:], ciphertext...), nil
}
