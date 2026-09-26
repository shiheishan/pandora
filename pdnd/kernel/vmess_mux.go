// [INPUT]: 依赖 vmess.go 的 vmessAdapter（经其 DataPlane 拨号）、vmess_codec.go 的 vmessBodyReader / newVMessAEADWriter / vmessWriteResponse、vmess_request.go 的 vmessDestination / vmessDestinationUDPAddr，依赖 sing 的 M.Socksaddr 地址编解码
// [OUTPUT]: 包内提供 vmessMuxSession / vmessMuxStream、handleMux 与 mux 帧的读写（readVMessMuxHeader / readVMessMuxData）
// [POS]: kernel 的 VMess 原生 mux：从 vmess.go 拆出，与 vless_mux.go 同一思路——mux 帧留在 NativeCore 内而不委托兼容内核；每条子流按 TCP / UDP 经 DataPlane 路由并计量
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	M "github.com/sagernet/sing/common/metadata"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
)

const (
	vmessMuxStatusNew       byte = 1
	vmessMuxStatusKeep      byte = 2
	vmessMuxStatusEnd       byte = 3
	vmessMuxStatusKeepAlive byte = 4
	vmessMuxOptionData      byte = 1
	vmessMuxOptionError     byte = 2
	vmessMuxNetworkTCP      byte = 1
	vmessMuxNetworkUDP      byte = 2
)

type vmessMuxSession struct {
	adapter *vmessAdapter
	ctx     context.Context
	user    core.User
	writer  io.Writer
	mu      sync.Mutex
	streams map[uint16]*vmessMuxStream
	wg      sync.WaitGroup
}

type vmessMuxStream struct {
	sessionID uint16
	network   byte
	dest      M.Socksaddr
	pipeW     *io.PipeWriter
	pipeR     *io.PipeReader
	closed    chan struct{}
}

func (a *vmessAdapter) handleMux(ctx context.Context, conn net.Conn, user core.User, body *vmessBodyReader, security byte) error {
	if err := vmessWriteResponse(conn, body.key, body.nonce, 0, body.option); err != nil {
		return err
	}
	var writer io.Writer = conn
	if security == vmessSecAES128 || security == vmessSecChaCha {
		keyHash := sha256.Sum256(body.key)
		nonceHash := sha256.Sum256(body.nonce)
		writer = newVMessAEADWriter(conn, vmessBodyAEAD(security, keyHash[:16]), nonceHash[:16], body.option)
	}
	session := &vmessMuxSession{adapter: a, ctx: ctx, user: user, writer: writer, streams: make(map[uint16]*vmessMuxStream)}
	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		streamID, status, option, network, destination, err := readVMessMuxHeader(reader)
		if err != nil {
			session.closeAll()
			session.wg.Wait()
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("vmess mux header: %w", err)
		}
		var stream *vmessMuxStream
		session.mu.Lock()
		stream = session.streams[streamID]
		session.mu.Unlock()
		switch status {
		case vmessMuxStatusNew:
			if stream != nil {
				return fmt.Errorf("vmess mux duplicate stream %d", streamID)
			}
			if network != vmessMuxNetworkTCP && network != vmessMuxNetworkUDP {
				return fmt.Errorf("vmess mux network %d unsupported", network)
			}
			stream = session.newStream(streamID, network, destination)
		case vmessMuxStatusKeep:
			if stream == nil {
				_ = session.writeClose(streamID, true)
			}
		case vmessMuxStatusEnd:
			if stream != nil {
				session.removeStream(streamID, option&vmessMuxOptionError != 0)
			}
		case vmessMuxStatusKeepAlive:
		default:
			return fmt.Errorf("vmess mux status %d unsupported", status)
		}
		if option&vmessMuxOptionData == 0 {
			continue
		}
		data, dataDestination, err := readVMessMuxData(reader, destination)
		if err != nil {
			session.closeAll()
			session.wg.Wait()
			return fmt.Errorf("vmess mux data: %w", err)
		}
		if stream == nil {
			continue
		}
		if dataDestination.IsValid() {
			stream.dest = dataDestination
		}
		if stream.network == vmessMuxNetworkTCP {
			if _, err := stream.pipeW.Write(data); err != nil {
				session.removeStream(streamID, true)
			}
		} else {
			session.wg.Add(1)
			go func(streamID uint16, stream *vmessMuxStream, payload []byte) {
				defer session.wg.Done()
				if err := session.forwardMuxUDP(stream, payload); err != nil {
					_ = session.writeClose(streamID, true)
					session.removeStream(streamID, true)
				}
			}(streamID, stream, data)
		}
	}
}

func (s *vmessMuxSession) newStream(id uint16, network byte, destination M.Socksaddr) *vmessMuxStream {
	reader, writer := io.Pipe()
	stream := &vmessMuxStream{sessionID: id, network: network, dest: destination, pipeW: writer, pipeR: reader, closed: make(chan struct{})}
	s.mu.Lock()
	s.streams[id] = stream
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if network == vmessMuxNetworkTCP {
			s.forwardMuxTCP(stream)
		}
	}()
	return stream
}

func (s *vmessMuxSession) forwardMuxTCP(stream *vmessMuxStream) {
	if !stream.dest.IsValid() {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "tcp", Protocol: "vmess-mux"}
	upstream, err := s.adapter.plane.DialTCP(s.ctx, meta, stream.dest)
	if err != nil {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	defer upstream.Close()
	conn := &vmessMuxTCPConn{stream: stream, session: s}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		_, _ = core.SpeedLimitedCopy(upstream, stream.pipeR, s.adapter.limiters.For(s.user))
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		wg.Done()
	}()
	go func() { _, _ = core.SpeedLimitedCopy(conn, upstream, s.adapter.limiters.For(s.user)); wg.Done() }()
	wg.Wait()
	_ = s.writeClose(stream.sessionID, false)
	s.removeStream(stream.sessionID, false)
}

type vmessMuxTCPConn struct {
	stream  *vmessMuxStream
	session *vmessMuxSession
}

func (c *vmessMuxTCPConn) Write(p []byte) (int, error) {
	return c.session.writeData(c.stream.sessionID, p)
}

func (c *vmessMuxTCPConn) Close() error {
	return c.session.writeClose(c.stream.sessionID, false)
}

func (s *vmessMuxSession) forwardMuxUDP(stream *vmessMuxStream, payload []byte) error {
	if !stream.dest.IsValid() {
		return fmt.Errorf("vmess mux UDP destination missing")
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "udp", Protocol: "vmess-mux"}
	upstream, err := s.adapter.plane.ListenUDP(s.ctx, meta, stream.dest)
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := vmessDestinationUDPAddr(vmessDestination{Host: stream.dest.AddrString(), Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port})
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
	responseDestination := stream.dest
	if sourceAddr != nil {
		if host, portText, splitErr := net.SplitHostPort(sourceAddr.String()); splitErr == nil {
			if portValue, parseErr := strconv.ParseUint(portText, 10, 16); parseErr == nil {
				responseDestination = M.ParseSocksaddrHostPort(host, uint16(portValue))
			}
		}
	}
	return s.writePacketData(stream.sessionID, response[:n], responseDestination)
}

func (s *vmessMuxSession) writeData(id uint16, data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) > 65535 {
		data = data[:65535]
	}
	var header [8]byte
	binary.BigEndian.PutUint16(header[0:2], 4)
	binary.BigEndian.PutUint16(header[2:4], id)
	header[4], header[5] = vmessMuxStatusKeep, vmessMuxOptionData
	binary.BigEndian.PutUint16(header[6:8], uint16(len(data)))
	if _, err := s.writer.Write(header[:]); err != nil {
		return 0, err
	}
	n, err := s.writer.Write(data)
	return n, err
}

func (s *vmessMuxSession) writePacketData(id uint16, data []byte, destination M.Socksaddr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var meta bytes.Buffer
	meta.WriteByte(vmessMuxNetworkUDP)
	if err := vmessAddressSerializer.WriteAddrPort(&meta, destination); err != nil {
		return err
	}
	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(meta.Len()+4))
	binary.BigEndian.PutUint16(header[2:4], id)
	header[4], header[5] = vmessMuxStatusKeep, vmessMuxOptionData
	if _, err := s.writer.Write(header[:]); err != nil {
		return err
	}
	if _, err := s.writer.Write(meta.Bytes()); err != nil {
		return err
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(data)))
	if _, err := s.writer.Write(length[:]); err != nil {
		return err
	}
	_, err := s.writer.Write(data)
	return err
}

func (s *vmessMuxSession) writeClose(id uint16, hasError bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var frame [6]byte
	binary.BigEndian.PutUint16(frame[0:2], 4)
	binary.BigEndian.PutUint16(frame[2:4], id)
	frame[4] = vmessMuxStatusEnd
	if hasError {
		frame[5] = vmessMuxOptionError
	}
	_, err := s.writer.Write(frame[:])
	return err
}

func (s *vmessMuxSession) removeStream(id uint16, withError bool) {
	s.mu.Lock()
	stream := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if stream != nil {
		close(stream.closed)
		_ = stream.pipeW.CloseWithError(func() error {
			if withError {
				return io.ErrUnexpectedEOF
			}
			return nil
		}())
	}
}

func (s *vmessMuxSession) closeAll() {
	s.mu.Lock()
	streams := make([]*vmessMuxStream, 0, len(s.streams))
	for id, stream := range s.streams {
		delete(s.streams, id)
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		close(stream.closed)
		_ = stream.pipeW.Close()
	}
}

func readVMessMuxHeader(reader io.Reader) (uint16, byte, byte, byte, M.Socksaddr, error) {
	var fixed [6]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, err
	}
	length := int(binary.BigEndian.Uint16(fixed[0:2]))
	if length < 4 || length > 4096 {
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("mux metadata length %d invalid", length)
	}
	meta := make([]byte, length-4)
	if _, err := io.ReadFull(reader, meta); err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, err
	}
	streamID := binary.BigEndian.Uint16(fixed[2:4])
	status, option := fixed[4], fixed[5]
	if len(meta) == 0 {
		return streamID, status, option, 0, M.Socksaddr{}, nil
	}
	network := meta[0]
	destination, err := vmessAddressSerializer.ReadAddrPort(bytes.NewReader(meta[1:]))
	if err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("mux destination bytes %x: %w", meta, err)
	}
	return streamID, status, option, network, destination, err
}

func readVMessMuxData(reader io.Reader, destination M.Socksaddr) ([]byte, M.Socksaddr, error) {
	var length [2]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, M.Socksaddr{}, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n > 65535 {
		return nil, M.Socksaddr{}, fmt.Errorf("mux data length invalid")
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, M.Socksaddr{}, err
	}
	return data, destination, nil
}
