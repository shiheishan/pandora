package tuic

import (
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/canceler"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
)

func (s *serverSession[U]) loopMessages() {
	select {
	case <-s.connDone:
		return
	case <-s.authDone:
	}
	for {
		message, err := s.quicConn.ReceiveDatagram(s.ctx)
		if err != nil {
			s.closeWithError(E.Cause(err, "receive message"))
			return
		}
		hErr := s.handleMessage(message)
		if hErr != nil {
			s.closeWithError(E.Cause(hErr, "handle message"))
			return
		}
	}
}

func (s *serverSession[U]) handleMessage(data []byte) error {
	if len(data) < 2 {
		return E.New("invalid message")
	}
	if data[0] != Version {
		return E.New("unknown version ", data[0])
	}
	switch data[1] {
	case CommandPacket:
		message := allocMessage()
		// loopMessages 是本连接唯一的 datagram 收包 goroutine，目标缓存不用加锁。
		err := decodeUDPMessage(message, data[2:], &s.destCache)
		if err != nil {
			message.release()
			return E.Cause(err, "decode UDP message")
		}
		s.handleUDPMessage(message, false)
		return nil
	case CommandHeartbeat:
		return nil
	default:
		return E.New("unknown command ", data[0])
	}
}

func (s *serverSession[U]) handleUDPMessage(message *udpMessage, udpStream bool) {
	s.udpAccess.RLock()
	udpConn, loaded := s.udpConnMap[message.sessionID]
	s.udpAccess.RUnlock()
	if !loaded || common.Done(udpConn.ctx) {
		// 关闭回调只认建会话这一刻的 sessionID 和这条 conn（Pandora 改动）：message 来自
		// 对象池，转发读完就归还清零、可能已被别的包复用，回调里再读 message.sessionID
		// 是数据竞争，会删掉别的会话（常见是 0 号）的登记；同号会话已被新 conn 替换时，
		// 旧 conn 关闭也不能把新的删掉。
		sessionID := message.sessionID
		var created *udpPacketConn
		created = newUDPPacketConn(auth.ContextWithUser(s.ctx, s.authUser), s.quicConn, udpStream, true, func() {
			s.udpAccess.Lock()
			if s.udpConnMap[sessionID] == created {
				delete(s.udpConnMap, sessionID)
			}
			s.udpAccess.Unlock()
		}, s.udpQueueSize)
		udpConn = created
		udpConn.sessionID = sessionID
		s.udpAccess.Lock()
		s.udpConnMap[sessionID] = udpConn
		s.udpAccess.Unlock()
		newCtx, newConn := canceler.NewPacketConn(udpConn.ctx, udpConn, s.udpTimeout)
		go s.handler.NewPacketConnectionEx(newCtx, newConn, M.SocksaddrFromNet(s.quicConn.RemoteAddr()).Unwrap(), message.destination, nil)
	}
	udpConn.inputPacket(message)
}
