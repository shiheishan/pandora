package hysteria2

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/internal/nativewire/dgram"
	"github.com/aegispanel/nodeagent/internal/nativewire/hysteria2/internal/protocol"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/quic-go/quicvarint"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
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
	sessionID     uint32
	packetID      uint16
	fragmentID    uint8
	fragmentTotal uint8
	destination   string
	// addr 是 destination 预先解析好的结果（hasAddr 为真时有效）。服务端按会话
	// 缓存最近的目标，同一目标的包不再逐包解析地址字符串（Pandora 改动）。
	addr       M.Socksaddr
	hasAddr    bool
	data       *buf.Buffer
	referenced bool
}

// socksaddr 返回消息目标；没有预解析时退回逐包解析。
func (m *udpMessage) socksaddr() M.Socksaddr {
	if m.hasAddr {
		return m.addr
	}
	return M.ParseSocksaddr(m.destination).Unwrap()
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

// appendTo 把消息编码追加到 dst：与原 pack 的线格式逐字节一致，但不经
// binary.Write 的反射与临时分配（Pandora 改动）。
func (m *udpMessage) appendTo(dst []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, m.sessionID)
	dst = binary.BigEndian.AppendUint16(dst, m.packetID)
	dst = append(dst, m.fragmentID, m.fragmentTotal)
	dst = quicvarint.Append(dst, uint64(len(m.destination)))
	dst = append(dst, m.destination...)
	return append(dst, m.data.Bytes()...)
}

func (m *udpMessage) headerSize() int {
	return 8 + int(quicvarint.Len(uint64(len(m.destination)))) + len(m.destination)
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
		/*if index > 0 {
			fragment.destination = ""
			// not work in hysteria
		}*/
	}
	return fragments
}

// DefaultUDPQueueSize 是每个 UDP 会话「已收到、待转发」的消息队列长度。
// 上游 sing-quic 写死 64，单连接 300Mbps（1200 字节包分两片，约 6 万片/秒）时
// 转发 goroutine 稍一停顿就整批丢；放大到 512 并可经 ServiceOptions 配置。
// 队列满仍然丢包（UDP 语义），不阻塞 QUIC 收包循环。
const DefaultUDPQueueSize = 512

type udpPacketConn struct {
	ctx             context.Context
	cancel          common.ContextCancelCauseFunc
	sessionID       uint32
	quicConn        *quic.Conn
	data            chan *udpMessage
	packetId        atomic.Uint32
	closeOnce       sync.Once
	defragger       *udpDefragger
	onDestroy       func()
	readWaitOptions N.ReadWaitOptions
	readDeadline    pipe.Deadline
	// dropped 统计因队列满丢弃的消息数，供测试与诊断。
	dropped atomic.Uint64
	// 空闲超时由连接自己维护（实现 canceler.PacketConn），见 SetTimeout。
	idle idleTimeout
	// lastWrite 缓存最近一次下行目标的字符串形式，同一目标不再逐包 String()。
	lastWrite atomic.Pointer[writeDestination]
	// datagram 跟踪单个 DATAGRAM 的实际上限，决定整包发还是分片（上游固定按
	// 1197 字节分片）。
	datagram dgram.Limit
}

type writeDestination struct {
	addr M.Socksaddr
	text string
}

func newUDPPacketConn(ctx context.Context, quicConn *quic.Conn, onDestroy func(), queueSize int) *udpPacketConn {
	ctx, cancel := common.ContextWithCancelCause(ctx)
	if queueSize <= 0 {
		queueSize = DefaultUDPQueueSize
	}
	return &udpPacketConn{
		ctx:          ctx,
		cancel:       cancel,
		quicConn:     quicConn,
		data:         make(chan *udpMessage, queueSize),
		defragger:    newUDPDefragger(),
		onDestroy:    onDestroy,
		readDeadline: pipe.MakeDeadline(),
	}
}

// Dropped 返回因接收队列满而丢弃的消息数。
func (c *udpPacketConn) Dropped() uint64 { return c.dropped.Load() }

// TryReadPacket 非阻塞地取一条已到达的消息，没有就返回 false。转发方先阻塞读
// 一条，再用它把队列里积压的消息一并取走，凑批发给上游。
func (c *udpPacketConn) TryReadPacket(buffer *buf.Buffer) (destination M.Socksaddr, ok bool) {
	select {
	case p := <-c.data:
		_, _ = buffer.ReadOnceFrom(p.data)
		destination = p.socksaddr()
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
		destination = p.socksaddr()
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
		destination := pkt.socksaddr()
		c.idle.touch()
		if destination.IsFqdn() {
			addr = destination
		} else {
			addr = destination.UDPAddr()
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
	if buffer.Len() > protocol.MaxUDPSize {
		return &quic.DatagramTooLargeError{MaxDatagramPayloadSize: protocol.MaxUDPSize}
	}
	packetId := uint16(c.packetId.Add(1) % math.MaxUint16)
	message := allocMessage()
	*message = udpMessage{
		sessionID:     c.sessionID,
		packetID:      packetId,
		fragmentTotal: 1,
		destination:   c.destinationText(destination),
		data:          buffer,
	}
	defer message.releaseMessage()
	c.idle.touch()
	return c.sendMessage(message)
}

func (c *udpPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	select {
	case <-c.ctx.Done():
		return 0, net.ErrClosed
	default:
	}
	if len(p) > protocol.MaxUDPSize {
		return 0, &quic.DatagramTooLargeError{MaxDatagramPayloadSize: protocol.MaxUDPSize}
	}
	packetId := uint16(c.packetId.Add(1) % math.MaxUint16)
	message := allocMessage()
	*message = udpMessage{
		sessionID:     c.sessionID,
		packetID:      packetId,
		fragmentTotal: 1,
		destination:   addr.String(),
		data:          buf.As(p),
	}
	if err = c.sendMessage(message); err != nil {
		return 0, err
	}
	return len(p), nil
}

// errDatagramLimit 是 quic-go 报的单个 DATAGRAM 上限小到装不下消息头、或要分出
// 超过 255 片（fragmentTotal 是单字节）：只有对端声明了异常小的
// max_datagram_frame_size 才会这样，丢掉这一包，不进分片循环。
var errDatagramLimit = errors.New("hysteria2: datagram limit too small to fragment")

// sendMessage 按连接的实际 DATAGRAM 上限发一条消息：装得下就整包发，否则分片
// （见 dgram.Limit；Pandora 改动）。
func (c *udpPacketConn) sendMessage(message *udpMessage) error {
	limit := c.datagram.Size(c.quicConn)
	if message.headerSize()+message.data.Len() > limit {
		return c.sendFragments(message, limit)
	}
	err := c.writePacket(message)
	var tooLargeErr *quic.DatagramTooLargeError
	if err == nil || !errors.As(err, &tooLargeErr) {
		return err
	}
	// 缓存的上限过时（PMTU 变小）：重新问一次再分片。
	return c.sendFragments(message, c.datagram.Refresh(c.quicConn))
}

func (c *udpPacketConn) sendFragments(message *udpMessage, maxPacketSize int) error {
	chunk := maxPacketSize - message.headerSize()
	if chunk <= 0 || (message.data.Len()+chunk-1)/chunk > math.MaxUint8 {
		return errDatagramLimit
	}
	return c.writePackets(fragUDPMessage(message, maxPacketSize))
}

// destinationText 返回目标地址的线格式字符串；与上一包目标相同时复用缓存。
func (c *udpPacketConn) destinationText(destination M.Socksaddr) string {
	if cached := c.lastWrite.Load(); cached != nil && cached.addr == destination {
		return cached.text
	}
	text := destination.String()
	c.lastWrite.Store(&writeDestination{addr: destination, text: text})
	return text
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
	// SendDatagram 会自己复制一份，编码缓冲放栈上即可（分片后单条不超过 MTU）。
	var scratch [1536]byte
	return c.quicConn.SendDatagram(message.appendTo(scratch[:0]))
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

func (c *udpPacketConn) ReaderMTU() int {
	return protocol.MaxUDPSize
}

func (c *udpPacketConn) WriterMTU() int {
	return protocol.MaxUDPSize
}

// defragSlots 是每个会话同时重组中的分片包上限。
const defragSlots = 16

// udpDefragger 重组分片消息（Pandora 改动）。上游用 10 秒寿命的 LRU，每个分片
// 都要 LoadOrStore、分配表项和链表节点；同一会话的分片几乎总是紧挨着到达，
// 这里改成按 packetID 线性查找的小环：最多同时重组 defragSlots 个包，新包挤掉
// 最旧的未完成包。内存因此按会话有界，乱发 packetID 也撑不大。
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
	releaseMessages(s.messages)
	for i := range s.messages {
		s.messages[i] = nil
	}
	s.messages = s.messages[:0]
	s.used, s.count = false, 0
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
		releaseMessages(slot.messages)
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
	newMessage.sessionID = m.sessionID
	newMessage.packetID = m.packetID
	newMessage.destination = slot.messages[0].destination
	newMessage.addr, newMessage.hasAddr = slot.messages[0].addr, slot.messages[0].hasAddr
	newMessage.data = buf.NewSize(finalLength)
	for _, message := range slot.messages {
		_, _ = newMessage.data.Write(message.data.Bytes())
	}
	slot.clear()
	return newMessage
}

// decodeUDPMessage 解析一条 UDP 消息。手写解析代替 bytes.Reader + binary.Read
// （每包多次分配）；destCache 非 nil 时（服务端单 goroutine 收包循环）同一目标
// 复用上一包的地址字符串与解析结果（Pandora 改动，线格式不变）。
func decodeUDPMessage(message *udpMessage, data []byte, destCache *destinationCache) error {
	if len(data) < 8 {
		return io.ErrUnexpectedEOF
	}
	message.sessionID = binary.BigEndian.Uint32(data[0:4])
	message.packetID = binary.BigEndian.Uint16(data[4:6])
	message.fragmentID = data[6]
	message.fragmentTotal = data[7]
	length, consumed, err := quicvarint.Parse(data[8:])
	if err != nil {
		return err
	}
	if length > protocol.MaxAddressLength {
		return errors.New("invalid address length")
	}
	rest := data[8+consumed:]
	if uint64(len(rest)) < length {
		return io.ErrUnexpectedEOF
	}
	raw := rest[:length]
	if destCache != nil {
		message.destination, message.addr = destCache.lookup(raw)
		message.hasAddr = true
	} else {
		message.destination = string(raw)
	}
	message.data = buf.As(rest[length:])
	return nil
}

// destinationCache 记住上一条消息的目标：同一会话的包通常发往同一目标。
// 只能在单个 goroutine 里用。
type destinationCache struct {
	text string
	addr M.Socksaddr
	set  bool
}

func (d *destinationCache) lookup(raw []byte) (string, M.Socksaddr) {
	if !d.set || d.text != string(raw) {
		d.text = string(raw)
		d.addr = M.ParseSocksaddr(d.text).Unwrap()
		d.set = true
	}
	return d.text, d.addr
}
