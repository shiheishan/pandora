package tuic

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/pipe"
)

var udpMessagePool = sync.Pool{
	New: func() interface{} {
		return new(udpMessage)
	},
}

func allocMessage() *udpMessage {
	message := udpMessagePool.Get().(*udpMessage)
	message.referenced = true
	return message
}

func releaseMessages(messages []*udpMessage) {
	for _, message := range messages {
		if message != nil {
			message.release()
		}
	}
}

type udpMessage struct {
	sessionID     uint16
	packetID      uint16
	fragmentTotal uint8
	fragmentID    uint8
	destination   M.Socksaddr
	data          *buf.Buffer
	referenced    bool
}

func (m *udpMessage) release() {
	if !m.referenced {
		return
	}
	*m = udpMessage{}
	udpMessagePool.Put(m)
}

func (m *udpMessage) releaseMessage() {
	m.data.Release()
	m.release()
}

// appendTo 把消息按 TUIC 线格式编码追加到 dst（Pandora 改动）：VER CMD ASSOC_ID
// PKT_ID FRAG_TOTAL FRAG_ID SIZE ADDR DATA，与原 pack（binary.Write +
// AddressSerializer）逐字节一致，但不经反射、不逐包分配缓冲。域名超过 255 字节
// 时返回错误（原 pack 在这里 panic）。
func (m *udpMessage) appendTo(dst []byte) ([]byte, error) {
	dst = append(dst, Version, CommandPacket)
	dst = binary.BigEndian.AppendUint16(dst, m.sessionID)
	dst = binary.BigEndian.AppendUint16(dst, m.packetID)
	dst = append(dst, m.fragmentTotal, m.fragmentID)
	dst = binary.BigEndian.AppendUint16(dst, uint16(m.data.Len()))
	dst, err := appendAddrPort(dst, m.destination)
	if err != nil {
		return nil, err
	}
	return append(dst, m.data.Bytes()...), nil
}

// TUIC 地址类型字节（与 AddressSerializer 的登记一致）。
const (
	addressTypeFqdn = 0x00
	addressTypeIPv4 = 0x01
	addressTypeIPv6 = 0x02
	addressTypeNone = 0xff
)

var errFqdnTooLong = errors.New("fqdn too long")

// appendAddrPort 与 AddressSerializer.WriteAddrPort 同格式：类型、地址，地址有效
// 时再跟 2 字节端口（无地址只有类型字节）。
func appendAddrPort(dst []byte, destination M.Socksaddr) ([]byte, error) {
	switch {
	case !destination.IsValid():
		return append(dst, addressTypeNone), nil
	case destination.IsIPv4():
		ip := destination.Addr.As4()
		dst = append(append(dst, addressTypeIPv4), ip[:]...)
	case destination.IsIPv6():
		ip := destination.Addr.As16()
		dst = append(append(dst, addressTypeIPv6), ip[:]...)
	default:
		if len(destination.Fqdn) > 255 {
			return nil, errFqdnTooLong
		}
		dst = append(dst, addressTypeFqdn, byte(len(destination.Fqdn)))
		dst = append(dst, destination.Fqdn...)
	}
	return binary.BigEndian.AppendUint16(dst, destination.Port), nil
}

// addrPortLen 返回 data 开头一个地址（含端口）的编码长度，并校验类型与长度。
func addrPortLen(data []byte) (int, error) {
	if len(data) < 1 {
		return 0, io.ErrUnexpectedEOF
	}
	var n int
	switch data[0] {
	case addressTypeNone:
		return 1, nil
	case addressTypeIPv4:
		n = 1 + 4
	case addressTypeIPv6:
		n = 1 + 16
	case addressTypeFqdn:
		if len(data) < 2 {
			return 0, io.ErrUnexpectedEOF
		}
		n = 2 + int(data[1])
	default:
		return 0, E.New("unknown address family: ", data[0])
	}
	if data[0] == addressTypeFqdn && n == 2 {
		// 空域名等于无效地址，原实现不读端口。
		return n, nil
	}
	if len(data) < n+2 {
		return 0, io.ErrUnexpectedEOF
	}
	return n + 2, nil
}

// parseAddrPort 解析 addrPortLen 校验过的一段地址，结果与
// AddressSerializer.ReadAddrPort 相同（IPv6 里的 4in6 拆成 IPv4，域名按字面
// 是 IP 的话解析成 IP）。
func parseAddrPort(raw []byte) M.Socksaddr {
	var addr M.Socksaddr
	switch raw[0] {
	case addressTypeNone:
		return M.Socksaddr{}
	case addressTypeIPv4:
		addr = M.Socksaddr{Addr: netip.AddrFrom4([4]byte(raw[1:5]))}
		raw = raw[5:]
	case addressTypeIPv6:
		addr = M.Socksaddr{Addr: netip.AddrFrom16([16]byte(raw[1:17]))}.Unwrap()
		raw = raw[17:]
	default:
		length := int(raw[1])
		addr = M.ParseSocksaddrHostPort(string(raw[2:2+length]), 0)
		raw = raw[2+length:]
	}
	if addr.IsValid() {
		addr.Port = binary.BigEndian.Uint16(raw)
	}
	return addr
}

// destinationCache 记住上一条消息的目标编码：同一会话的包通常发往同一目标，
// 域名目标不必逐包分配字符串、重新解析。只能在单个 goroutine 里用（服务端的
// datagram 收包循环）。
type destinationCache struct {
	raw  string
	addr M.Socksaddr
	set  bool
}

func (d *destinationCache) lookup(raw []byte) M.Socksaddr {
	if !d.set || d.raw != string(raw) {
		d.raw = string(raw)
		d.addr = parseAddrPort(raw)
		d.set = true
	}
	return d.addr
}

func (m *udpMessage) headerSize() int {
	return 10 + AddressSerializer.AddrPortLen(m.destination)
}

func fragUDPMessage(message *udpMessage, maxPacketSize int) []*udpMessage {
	udpMTU := maxPacketSize - message.headerSize()
	if message.data.Len() <= udpMTU {
		return []*udpMessage{message}
	}
	var fragments []*udpMessage
	originPacket := message.data.Bytes()
	for remaining := len(originPacket); remaining > 0; remaining -= udpMTU {
		fragment := allocMessage()
		*fragment = *message
		if remaining > udpMTU {
			fragment.data = buf.As(originPacket[:udpMTU])
			originPacket = originPacket[udpMTU:]
		} else {
			fragment.data = buf.As(originPacket)
			originPacket = nil
		}
		fragments = append(fragments, fragment)
	}
	fragmentTotal := uint16(len(fragments))
	for index, fragment := range fragments {
		fragment.fragmentID = uint8(index)
		fragment.fragmentTotal = uint8(fragmentTotal)
		if index > 0 {
			fragment.destination = M.Socksaddr{}
		}
	}
	return fragments
}

var (
	_ N.NetPacketConn    = (*udpPacketConn)(nil)
	_ N.PacketReadWaiter = (*udpPacketConn)(nil)
)

// DefaultUDPQueueSize 是每个 UDP 会话「已收到、待转发」的消息队列长度
// （Pandora 改动，同 nativewire/hysteria2）。上游写死 64，转发 goroutine 稍一
// 停顿就整批丢；放大到 512，非正值的 ServiceOptions.UDPQueueSize 用它。
// 队列满仍然丢包（UDP 语义），不阻塞 QUIC 收包循环。
const DefaultUDPQueueSize = 512

type udpPacketConn struct {
	ctx             context.Context
	cancel          common.ContextCancelCauseFunc
	sessionID       uint16
	quicConn        *quic.Conn
	data            chan *udpMessage
	udpStream       bool
	udpMTU          int
	packetId        atomic.Uint32
	closeOnce       sync.Once
	isServer        bool
	defragger       *udpDefragger
	onDestroy       func()
	readWaitOptions N.ReadWaitOptions
	readDeadline    pipe.Deadline
	// dropped 统计因队列满丢弃的消息数，供测试与诊断。
	dropped atomic.Uint64
	// 空闲超时由连接自己维护（实现 canceler.PacketConn），见 idle.go。
	idle idleTimeout
}

func newUDPPacketConn(ctx context.Context, quicConn *quic.Conn, udpStream bool, isServer bool, onDestroy func(), queueSize int) *udpPacketConn {
	ctx, cancel := common.ContextWithCancelCause(ctx)
	if queueSize <= 0 {
		queueSize = DefaultUDPQueueSize
	}
	return &udpPacketConn{
		ctx:          ctx,
		cancel:       cancel,
		quicConn:     quicConn,
		data:         make(chan *udpMessage, queueSize),
		udpStream:    udpStream,
		isServer:     isServer,
		defragger:    newUDPDefragger(),
		onDestroy:    onDestroy,
		udpMTU:       1200 - 3,
		readDeadline: pipe.MakeDeadline(),
	}
}

// Dropped 返回因接收队列满而丢弃的消息数。
func (c *udpPacketConn) Dropped() uint64 { return c.dropped.Load() }

// TryReadPacket 非阻塞地取一条已到达的消息，没有就返回 false。转发方先阻塞读
// 一条，再用它把队列里积压的消息一并取走，凑批发给上游（Pandora 改动）。
func (c *udpPacketConn) TryReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, ok bool) {
	select {
	case p := <-c.data:
		_, _ = buffer.ReadOnceFrom(p.data)
		destination = p.destination
		p.releaseMessage()
		c.idle.touch()
		return destination, true
	default:
		return M.Socksaddr{}, false
	}
}

func (c *udpPacketConn) ReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, err error) {
	select {
	case p := <-c.data:
		_, err = buffer.ReadOnceFrom(p.data)
		destination = p.destination
		p.releaseMessage()
		c.idle.touch()
		return
	case <-c.ctx.Done():
		return M.Socksaddr{}, io.ErrClosedPipe
	case <-c.readDeadline.Wait():
		return M.Socksaddr{}, os.ErrDeadlineExceeded
	}
}

func (c *udpPacketConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	select {
	case pkt := <-c.data:
		n = copy(p, pkt.data.Bytes())
		c.idle.touch()
		if pkt.destination.IsFqdn() {
			addr = pkt.destination
		} else {
			addr = pkt.destination.UDPAddr()
		}
		pkt.releaseMessage()
		return n, addr, nil
	case <-c.ctx.Done():
		return 0, nil, io.ErrClosedPipe
	case <-c.readDeadline.Wait():
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (c *udpPacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	defer buffer.Release()
	select {
	case <-c.ctx.Done():
		return net.ErrClosed
	default:
	}
	if buffer.Len() > 0xffff {
		return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: 0xffff}
	}
	if !destination.IsValid() {
		return E.New("invalid destination address")
	}
	packetId := uint16(c.packetId.Add(1) % math.MaxUint16)
	message := allocMessage()
	*message = udpMessage{
		sessionID:     c.sessionID,
		packetID:      packetId,
		fragmentTotal: 1,
		destination:   destination,
		data:          buffer,
	}
	defer message.releaseMessage()
	c.idle.touch()
	var err error
	if !c.udpStream && buffer.Len() > c.udpMTU-message.headerSize() {
		err = c.writePackets(fragUDPMessage(message, c.udpMTU))
	} else {
		err = c.writePacket(message)
	}
	if err == nil {
		return nil
	}
	var tooLargeErr *quic.DatagramTooLargeError
	if !errors.As(err, &tooLargeErr) {
		return err
	}
	c.udpMTU = int(tooLargeErr.MaxDatagramPayloadSize) - 3
	return c.writePackets(fragUDPMessage(message, c.udpMTU))
}

func (c *udpPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, net.ErrClosed
	default:
	}
	if len(p) > 0xffff {
		return 0, &quic.DatagramTooLargeError{MaxDatagramPayloadSize: 0xffff}
	}
	destination := M.SocksaddrFromNet(addr)
	if !destination.IsValid() {
		return 0, E.New("invalid destination address")
	}
	packetId := uint16(c.packetId.Add(1) % math.MaxUint16)
	message := allocMessage()
	*message = udpMessage{
		sessionID:     c.sessionID,
		packetID:      packetId,
		fragmentTotal: 1,
		destination:   destination,
		data:          buf.As(p),
	}
	c.idle.touch()
	if !c.udpStream && len(p) > c.udpMTU-message.headerSize() {
		err = c.writePackets(fragUDPMessage(message, c.udpMTU))
		if err == nil {
			return len(p), nil
		}
	} else {
		err = c.writePacket(message)
	}
	if err == nil {
		return len(p), nil
	}
	var tooLargeErr *quic.DatagramTooLargeError
	if !errors.As(err, &tooLargeErr) {
		return
	}
	c.udpMTU = int(tooLargeErr.MaxDatagramPayloadSize) - 3
	err = c.writePackets(fragUDPMessage(message, c.udpMTU))
	if err == nil {
		return len(p), nil
	}
	return
}

func (c *udpPacketConn) inputPacket(message *udpMessage) {
	if message.fragmentTotal > 1 {
		message = c.defragger.feed(message)
		if message == nil {
			return
		}
	}
	select {
	case c.data <- message:
	default:
		// 队列满：丢弃并归还缓冲（上游这里直接丢掉引用，池里的对象就漏了）。
		c.dropped.Add(1)
		message.releaseMessage()
	}
}

func (c *udpPacketConn) writePackets(messages []*udpMessage) error {
	defer releaseMessages(messages)
	for _, message := range messages {
		err := c.writePacket(message)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *udpPacketConn) writePacket(message *udpMessage) error {
	if !c.udpStream {
		// SendDatagram 会自己复制一份，编码缓冲放栈上即可（分片后单条不超过
		// MTU；万一超过，append 自己会挪到堆上）。
		var scratch [1536]byte
		packet, err := message.appendTo(scratch[:0])
		if err != nil {
			return err
		}
		return c.quicConn.SendDatagram(packet)
	}
	stream, err := c.quicConn.OpenUniStream()
	if err != nil {
		return err
	}
	packet, err := message.appendTo(make([]byte, 0, message.headerSize()+message.data.Len()))
	if err == nil {
		_, err = stream.Write(packet)
	}
	stream.Close()
	return err
}

func (c *udpPacketConn) Close() error {
	c.closeOnce.Do(func() {
		c.idle.stop()
		c.closeWithError(os.ErrClosed)
		c.onDestroy()
	})
	return nil
}

func (c *udpPacketConn) closeWithError(err error) {
	c.cancel(err)
	if !c.isServer {
		buffer := buf.NewSize(4)
		defer buffer.Release()
		buffer.WriteByte(Version)
		buffer.WriteByte(CommandDissociate)
		binary.Write(buffer, binary.BigEndian, c.sessionID)
		sendStream, openErr := c.quicConn.OpenUniStream()
		if openErr != nil {
			return
		}
		defer sendStream.Close()
		sendStream.Write(buffer.Bytes())
	}
}

func (c *udpPacketConn) LocalAddr() net.Addr {
	return c.quicConn.LocalAddr()
}

func (c *udpPacketConn) SetDeadline(t time.Time) error {
	return os.ErrInvalid
}

func (c *udpPacketConn) SetReadDeadline(t time.Time) error {
	c.readDeadline.Set(t)
	return nil
}

func (c *udpPacketConn) SetWriteDeadline(t time.Time) error {
	return os.ErrInvalid
}

// defragSlots 是每个会话同时重组中的分片包上限。
const defragSlots = 16

// udpDefragger 重组分片消息（Pandora 改动，同 nativewire/hysteria2）。上游用 10 秒
// 寿命的 LRU，每个分片都要 LoadOrStore、分配表项和链表节点；同一会话的分片几乎
// 总是紧挨着到达，这里改成按 packetID 线性查找的小环：最多同时重组 defragSlots
// 个包，新包挤掉最旧的未完成包。内存因此按会话有界，乱发 packetID 也撑不大。
// datagram 与 uni stream 两条收包路径会并发喂它，所以带锁。
type udpDefragger struct {
	mu    sync.Mutex
	slots [defragSlots]defragSlot
	next  int
}

type defragSlot struct {
	used     bool
	packetID uint16
	count    uint8
	messages []*udpMessage
}

func (s *defragSlot) clear() {
	releaseMessageData(s.messages)
	for i := range s.messages {
		s.messages[i] = nil
	}
	s.messages = s.messages[:0]
	s.used, s.count = false, 0
}

// releaseMessageData 连同负载缓冲一起归还（分片各自持有从池里取的缓冲）。
func releaseMessageData(messages []*udpMessage) {
	for _, message := range messages {
		if message != nil {
			message.releaseMessage()
		}
	}
}

func newUDPDefragger() *udpDefragger {
	return &udpDefragger{}
}

func (d *udpDefragger) slotFor(packetID uint16) *defragSlot {
	for i := range d.slots {
		if d.slots[i].used && d.slots[i].packetID == packetID {
			return &d.slots[i]
		}
	}
	slot := &d.slots[d.next]
	d.next = (d.next + 1) % defragSlots
	slot.clear()
	slot.used, slot.packetID = true, packetID
	return slot
}

func (d *udpDefragger) feed(m *udpMessage) *udpMessage {
	if m.fragmentTotal <= 1 {
		return m
	}
	if m.fragmentID >= m.fragmentTotal {
		m.releaseMessage()
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	slot := d.slotFor(m.packetID)
	if int(m.fragmentTotal) != len(slot.messages) {
		releaseMessageData(slot.messages)
		if cap(slot.messages) >= int(m.fragmentTotal) {
			slot.messages = slot.messages[:m.fragmentTotal]
			for i := range slot.messages {
				slot.messages[i] = nil
			}
		} else {
			slot.messages = make([]*udpMessage, m.fragmentTotal)
		}
		slot.count = 0
	}
	if slot.messages[m.fragmentID] != nil {
		m.releaseMessage()
		return nil
	}
	slot.messages[m.fragmentID] = m
	slot.count++
	if int(slot.count) != len(slot.messages) {
		return nil
	}
	var finalLength int
	for _, message := range slot.messages {
		finalLength += message.data.Len()
	}
	if finalLength == 0 {
		slot.clear()
		return nil
	}
	newMessage := allocMessage()
	*newMessage = *slot.messages[0]
	newMessage.referenced = true
	newMessage.data = buf.NewSize(finalLength)
	for _, message := range slot.messages {
		_, _ = newMessage.data.Write(message.data.Bytes())
	}
	slot.clear()
	return newMessage
}

func readUDPMessage(message *udpMessage, reader io.Reader) error {
	err := binary.Read(reader, binary.BigEndian, &message.sessionID)
	if err != nil {
		return err
	}
	err = binary.Read(reader, binary.BigEndian, &message.packetID)
	if err != nil {
		return err
	}
	err = binary.Read(reader, binary.BigEndian, &message.fragmentTotal)
	if err != nil {
		return err
	}
	err = binary.Read(reader, binary.BigEndian, &message.fragmentID)
	if err != nil {
		return err
	}
	var dataLength uint16
	err = binary.Read(reader, binary.BigEndian, &dataLength)
	if err != nil {
		return err
	}
	message.destination, err = AddressSerializer.ReadAddrPort(reader)
	if err != nil {
		return err
	}
	message.data = buf.NewSize(int(dataLength))
	_, err = message.data.ReadFullFrom(reader, message.data.FreeLen())
	if err != nil {
		return err
	}
	return nil
}

// decodeUDPMessage 解析一条 datagram 里的 UDP 消息（data 已去掉 VER、CMD）。
// 手写解析代替 bytes.Reader + binary.Read（Pandora 改动，线格式与校验不变：
// SIZE 必须与剩余负载长度相等）；destCache 非 nil 时（服务端单 goroutine 收包
// 循环）同一目标复用上一包的解析结果。
func decodeUDPMessage(message *udpMessage, data []byte, destCache *destinationCache) error {
	if len(data) < 8 {
		return io.ErrUnexpectedEOF
	}
	message.sessionID = binary.BigEndian.Uint16(data[0:2])
	message.packetID = binary.BigEndian.Uint16(data[2:4])
	message.fragmentTotal = data[4]
	message.fragmentID = data[5]
	dataLength := int(binary.BigEndian.Uint16(data[6:8]))
	rest := data[8:]
	addrLen, err := addrPortLen(rest)
	if err != nil {
		return err
	}
	if destCache != nil {
		message.destination = destCache.lookup(rest[:addrLen])
	} else {
		message.destination = parseAddrPort(rest[:addrLen])
	}
	if len(rest)-addrLen != dataLength {
		return io.ErrUnexpectedEOF
	}
	message.data = buf.As(rest[addrLen:])
	return nil
}
