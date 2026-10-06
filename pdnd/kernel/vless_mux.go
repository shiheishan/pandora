package kernel

// Native VLESS XUDP/mux framing.
//
// This is deliberately kept in the NativeCore package instead of delegating
// to a complete sing-box/xray runtime.  The wire format is the small mux
// envelope used by VLESS clients: a stream id, lifecycle status, network and
// optional address followed by a length-prefixed payload.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

const (
	vlessMuxStatusNew       byte = 1
	vlessMuxStatusKeep      byte = 2
	vlessMuxStatusEnd       byte = 3
	vlessMuxStatusKeepAlive byte = 4
	vlessMuxOptionData      byte = 1
	vlessMuxOptionError     byte = 2
	vlessMuxNetworkTCP      byte = 1
	vlessMuxNetworkUDP      byte = 2
)

type vlessMuxSession struct {
	adapter *vlessAdapter
	ctx     context.Context
	user    core.User
	writer  *bufio.Writer
	mu      sync.Mutex
	streams map[uint16]*vlessMuxStream
	wg      sync.WaitGroup
}

type vlessMuxStream struct {
	sessionID uint16
	network   byte
	dest      M.Socksaddr
	pipeW     *io.PipeWriter
	pipeR     *io.PipeReader
}

func (a *vlessAdapter) handleVLESSMux(ctx context.Context, conn net.Conn, user core.User, realitySession *RealitySession, remoteIPValue string) error {
	s := &vlessMuxSession{
		adapter: a, ctx: ctx, user: user,
		writer:  bufio.NewWriterSize(conn, 32<<10),
		streams: make(map[uint16]*vlessMuxStream),
	}
	// The VLESS response header was emitted before entering this method. Keep
	// the mux writer independent so concurrent stream responses cannot intermix.
	reader := bufio.NewReaderSize(conn, 64<<10)
	for {
		streamID, status, option, network, destination, err := readVLESSMuxHeader(reader)
		if err != nil {
			s.closeAll()
			s.wg.Wait()
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("vless mux header: %w", err)
		}
		s.mu.Lock()
		stream := s.streams[streamID]
		s.mu.Unlock()
		switch status {
		case vlessMuxStatusNew:
			if stream != nil {
				return fmt.Errorf("vless mux duplicate stream %d", streamID)
			}
			if network != vlessMuxNetworkTCP && network != vlessMuxNetworkUDP {
				return fmt.Errorf("vless mux network %d unsupported", network)
			}
			stream = s.newStream(streamID, network, destination, remoteIPValue, realitySession != nil)
		case vlessMuxStatusKeep:
			if stream == nil {
				_ = s.writeClose(streamID, true)
			}
		case vlessMuxStatusEnd:
			if stream != nil {
				s.removeStream(streamID, option&vlessMuxOptionError != 0)
			}
		case vlessMuxStatusKeepAlive:
		default:
			return fmt.Errorf("vless mux status %d unsupported", status)
		}
		if option&vlessMuxOptionData == 0 {
			continue
		}
		data, dataDestination, err := readVLESSMuxData(reader, destination)
		if err != nil {
			s.closeAll()
			s.wg.Wait()
			return fmt.Errorf("vless mux data: %w", err)
		}
		if stream == nil {
			continue
		}
		// TCP destinations are fixed by the NEW frame and are read by the
		// forwarding goroutine immediately; do not rewrite that field from the
		// receive loop. UDP streams may legitimately override it per packet.
		if stream.network == vlessMuxNetworkUDP && dataDestination.IsValid() {
			stream.dest = dataDestination
		}
		if stream.network == vlessMuxNetworkTCP {
			if _, err := stream.pipeW.Write(data); err != nil {
				s.removeStream(streamID, true)
			}
		} else {
			s.wg.Add(1)
			go func(id uint16, st *vlessMuxStream, payload []byte) {
				defer s.wg.Done()
				if err := s.forwardUDP(st, payload); err != nil {
					_ = s.writeClose(id, true)
					s.removeStream(id, true)
				}
			}(streamID, stream, data)
		}
	}
}

func (s *vlessMuxSession) newStream(id uint16, network byte, destination M.Socksaddr, remoteIPValue string, reality bool) *vlessMuxStream {
	pipeR, pipeW := io.Pipe()
	stream := &vlessMuxStream{sessionID: id, network: network, dest: destination, pipeW: pipeW, pipeR: pipeR}
	s.mu.Lock()
	s.streams[id] = stream
	s.mu.Unlock()
	if network == vlessMuxNetworkTCP {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.forwardTCP(stream, remoteIPValue, reality)
		}()
	}
	return stream
}

func (s *vlessMuxSession) forwardTCP(stream *vlessMuxStream, remoteIPValue string, reality bool) {
	if !stream.dest.IsValid() {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "tcp", Protocol: "vless-mux"}
	if reality {
		meta.Protocol = "vless-reality-mux"
	}
	if source, err := netip.ParseAddr(remoteIPValue); err == nil {
		meta.SourceIP = source
	}
	upstream, err := s.adapter.plane.DialTCP(s.ctx, meta, stream.dest)
	if err != nil {
		_ = s.writeClose(stream.sessionID, true)
		s.removeStream(stream.sessionID, true)
		return
	}
	defer upstream.Close()
	client := &vlessMuxTCPConn{stream: stream, session: s}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		_, _ = core.SpeedLimitedCopy(upstream, stream.pipeR, s.adapter.limiters.For(s.user))
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		wg.Done()
	}()
	go func() { _, _ = core.SpeedLimitedCopy(client, upstream, s.adapter.limiters.For(s.user)); wg.Done() }()
	wg.Wait()
	_ = s.writeClose(stream.sessionID, false)
	s.removeStream(stream.sessionID, false)
}

type vlessMuxTCPConn struct {
	stream  *vlessMuxStream
	session *vlessMuxSession
}

func (c *vlessMuxTCPConn) Write(p []byte) (int, error) {
	return c.session.writeData(c.stream.sessionID, p)
}

func (c *vlessMuxTCPConn) Close() error {
	return c.session.writeClose(c.stream.sessionID, false)
}

func (s *vlessMuxSession) forwardUDP(stream *vlessMuxStream, payload []byte) error {
	if !stream.dest.IsValid() {
		return fmt.Errorf("vless mux UDP destination missing")
	}
	meta := route.Meta{Domain: stream.dest.Fqdn, IP: stream.dest.Addr, Port: stream.dest.Port, Network: "udp", Protocol: "vless-mux"}
	upstream, err := s.adapter.plane.ListenUDP(s.ctx, meta, stream.dest)
	if err != nil {
		return err
	}
	defer upstream.Close()
	destinationAddr, err := vlessDestinationUDPAddr(stream.dest)
	if err != nil {
		return err
	}
	if _, err := upstream.WriteTo(payload, destinationAddr); err != nil {
		return err
	}
	_ = upstream.SetReadDeadline(time.Now().Add(2 * time.Second))
	response := make([]byte, vlessUDPMaxPacket)
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
	s.adapter.addTraffic(s.user, int64(len(payload)), int64(n))
	return s.writePacketData(stream.sessionID, response[:n], responseDestination)
}

func (s *vlessMuxSession) writeData(id uint16, data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) > vlessUDPMaxPacket {
		data = data[:vlessUDPMaxPacket]
	}
	var frame [8]byte
	binary.BigEndian.PutUint16(frame[0:2], 4)
	binary.BigEndian.PutUint16(frame[2:4], id)
	frame[4], frame[5] = vlessMuxStatusKeep, vlessMuxOptionData
	binary.BigEndian.PutUint16(frame[6:8], uint16(len(data)))
	if _, err := s.writer.Write(frame[:]); err != nil {
		return 0, err
	}
	n, err := s.writer.Write(data)
	if err == nil {
		err = s.writer.Flush()
	}
	return n, err
}

func (s *vlessMuxSession) writePacketData(id uint16, data []byte, destination M.Socksaddr) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var meta bytes.Buffer
	meta.WriteByte(vlessMuxNetworkUDP)
	if err := vmessAddressSerializer.WriteAddrPort(&meta, destination); err != nil {
		return err
	}
	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(meta.Len()+4))
	binary.BigEndian.PutUint16(header[2:4], id)
	header[4], header[5] = vlessMuxStatusKeep, vlessMuxOptionData
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
	if _, err := s.writer.Write(data); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *vlessMuxSession) writeClose(id uint16, hasError bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var frame [6]byte
	binary.BigEndian.PutUint16(frame[0:2], 4)
	binary.BigEndian.PutUint16(frame[2:4], id)
	frame[4] = vlessMuxStatusEnd
	if hasError {
		frame[5] = vlessMuxOptionError
	}
	if _, err := s.writer.Write(frame[:]); err != nil {
		return err
	}
	return s.writer.Flush()
}

func (s *vlessMuxSession) removeStream(id uint16, withError bool) {
	s.mu.Lock()
	stream := s.streams[id]
	delete(s.streams, id)
	s.mu.Unlock()
	if stream != nil {
		_ = stream.pipeW.CloseWithError(func() error {
			if withError {
				return io.ErrUnexpectedEOF
			}
			return nil
		}())
	}
}

func (s *vlessMuxSession) closeAll() {
	s.mu.Lock()
	streams := make([]*vlessMuxStream, 0, len(s.streams))
	for id, stream := range s.streams {
		delete(s.streams, id)
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	for _, stream := range streams {
		_ = stream.pipeW.Close()
	}
}

func readVLESSMuxHeader(reader io.Reader) (uint16, byte, byte, byte, M.Socksaddr, error) {
	var fixed [6]byte
	if _, err := io.ReadFull(reader, fixed[:]); err != nil {
		return 0, 0, 0, 0, M.Socksaddr{}, err
	}
	length := int(binary.BigEndian.Uint16(fixed[0:2]))
	if length < 4 || length > 4096 {
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("vless mux metadata length %d invalid", length)
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
		return 0, 0, 0, 0, M.Socksaddr{}, fmt.Errorf("vless mux destination bytes %x: %w", meta, err)
	}
	return streamID, status, option, network, destination, nil
}

func readVLESSMuxData(reader io.Reader, destination M.Socksaddr) ([]byte, M.Socksaddr, error) {
	var length [2]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, M.Socksaddr{}, err
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	data := make([]byte, n)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, M.Socksaddr{}, err
	}
	return data, destination, nil
}

func vlessDestinationUDPAddr(destination M.Socksaddr) (net.Addr, error) {
	if destination.Addr.IsValid() {
		return &net.UDPAddr{IP: net.IP(destination.Addr.AsSlice()), Port: int(destination.Port)}, nil
	}
	return net.ResolveUDPAddr("udp", net.JoinHostPort(destination.Fqdn, strconv.Itoa(int(destination.Port))))
}
