package mkcp

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Connection 是一条 mKCP 连接，实现 net.Conn。
//
// 把前面几块串起来：接收窗口负责拼装、发送窗口负责重传、拥塞窗口给额度、
// RTT 估算给超时。这里加的是时间驱动——一个后台循环按固定间隔调 flush，
// 推进重传、回 ACK、发心跳、走关闭流程。
//
// # 为什么用轮询而不是纯事件驱动
//
// 重传本来就是时间驱动的：没有任何事件能告诉你「这个段丢了」，只能等到
// 超时。既然必须有定时器，索性让它统一驱动所有周期性动作，比事件唤醒
// 加定时器两套机制并存要好推理。有数据要发时会额外唤醒一次，不必等下一
// 个 tick。

var (
	ErrConnClosed  = errors.New("mkcp: 连接已关闭")
	ErrTimeout     = &timeoutError{}
	ErrWriteTooBig = errors.New("mkcp: 单次写入超过分段上限")
)

type timeoutError struct{}

func (*timeoutError) Error() string   { return "mkcp: 操作超时" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

// Config 是一条连接的可调参数。零值不可用，用 DefaultConfig。
type Config struct {
	// MTU 是单个 UDP 包的字节上限。默认 1350——常见路径 MTU 是 1500，
	// 留出余量给外层可能叠加的封装（我们的 mKCP 通常跑在别的协议下面）。
	MTU uint32
	// SendingWindowSize 是发送缓冲能攒多少个段。
	SendingWindowSize uint32
	// ReceivingWindowSize 是接收侧能缓存多少个乱序段。
	ReceivingWindowSize uint32
	// InFlightSize 是拥塞窗口的基准（段数）。
	InFlightSize uint32
	// Congestion 打开后按丢包率动态调整在途额度。
	Congestion bool
	// Tick 是后台循环的间隔。太长会让重传迟钝，太短纯烧 CPU。
	Tick time.Duration
	// SocketBuffer 是底层 UDP socket 收发缓冲区的字节数。
	//
	// 这个值不设的后果比看上去严重：内核默认的几百 KB 在突发时一冲就满，
	// 满了内核直接丢包，而我们这层完全看不见——只表现为「莫名其妙的
	// 超时重传」。同一份数据在回环上传输，设与不设能差出十几倍耗时。
	SocketBuffer int
}

func DefaultConfig() *Config {
	return &Config{
		MTU:                 1350,
		SendingWindowSize:   1024,
		ReceivingWindowSize: 1024,
		InFlightSize:        128,
		Congestion:          false,
		Tick:                50 * time.Millisecond,
		SocketBuffer:        2 * 1024 * 1024,
	}
}

// MSS 是一个数据段能装的净荷字节数。
func (c *Config) MSS() uint32 {
	if c.MTU <= DataSegmentOverhead {
		return 1
	}
	return c.MTU - DataSegmentOverhead
}

// ConnMetadata 标识一条连接。
type ConnMetadata struct {
	LocalAddr    net.Addr
	RemoteAddr   net.Addr
	Conversation uint16
}

type Connection struct {
	meta   ConnMetadata
	config *Config
	// output 把段写到网络上。
	output SegmentWriter
	// closer 释放底层资源（监听器上的会话表项、或者拨号出来的 socket）。
	closer io.Closer

	mu   sync.Mutex
	cond *sync.Cond

	state State
	// stateSince 是进入当前状态的时刻，用来做各状态的超时兜底。
	stateSince timestamp
	// lastIncoming 是最后一次收到对端任何东西的时刻，用于空闲超时。
	lastIncoming timestamp
	lastPing     timestamp

	since time.Time

	sending   *SendingWindow
	receiving *ReceivingWindow
	acks      *AckList
	roundTrip *RoundTripInfo
	cwnd      *CongestionWindow

	// nextNumber 是下一个要分配的发送序号。
	nextNumber uint32
	// peerReceivingNext 是对端告知的「它期待的下一个序号」，用来算
	// 还能塞多少个段进去。
	peerReceivingNext uint32
	// readBuf 是已按序交付、等待上层读走的字节。
	readBuf []byte

	readDeadline  time.Time
	writeDeadline time.Time

	closeOnce sync.Once
	done      chan struct{}
}

// 各状态的兜底超时。都是「对端不配合时也要能收场」的保险。
const (
	// idleTimeout 内没收到对端任何东西就主动关闭。UDP 上对端可能直接
	// 消失，不设这个的话会话表会一直泄漏。
	idleTimeout = 30000
	// terminatingTimeout 是发 Terminate 后等对端确认的上限。
	terminatingTimeout = 8000
	// peerTerminatingTimeout 是给对端收尾的时间。
	peerTerminatingTimeout = 4000
	// readyToCloseTimeout 是等待未发完数据送出的上限。超了就不等了——
	// 对端可能已经不在了，不能为几个段一直挂着。
	readyToCloseTimeout = 15000
	// pingInterval 是心跳间隔。它同时承担保活和交换 ReceivingNext/RTO。
	pingInterval = 3000
)

func NewConnection(meta ConnMetadata, output SegmentWriter, closer io.Closer, config *Config) *Connection {
	if config == nil {
		config = DefaultConfig()
	}
	c := &Connection{
		meta: meta, config: config, output: output, closer: closer,
		since:             time.Now(),
		roundTrip:         &RoundTripInfo{},
		cwnd:              NewCongestionWindow(config.InFlightSize, config.Congestion),
		receiving:         NewReceivingWindow(config.ReceivingWindowSize),
		peerReceivingNext: config.InFlightSize,
		done:              make(chan struct{}),
	}
	c.cond = sync.NewCond(&c.mu)
	c.sending = NewSendingWindow(output, c.cwnd.OnPacketLoss)
	c.acks = NewAckList(output, config.MSS())
	go c.updateLoop()
	return c
}

// Elapsed 是连接建立以来的毫秒数，所有段里的时间戳都用它。
func (c *Connection) Elapsed() timestamp {
	return timestamp(time.Since(c.since).Milliseconds())
}

func (c *Connection) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// setState 切状态并唤醒所有等待者。调用方须持锁。
func (c *Connection) setState(s State) {
	if c.state == s {
		return
	}
	c.state = s
	c.stateSince = c.Elapsed()
	c.cond.Broadcast()
}

//------------------------------------------------------------------------------
// net.Conn
//------------------------------------------------------------------------------

func (c *Connection) Read(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if len(c.readBuf) > 0 {
			n := copy(b, c.readBuf)
			c.readBuf = c.readBuf[n:]
			return n, nil
		}
		if c.state.closedForRead() {
			return 0, io.EOF
		}
		if err := c.waitLocked(c.readDeadline); err != nil {
			return 0, err
		}
	}
}

func (c *Connection) Write(b []byte) (int, error) {
	mss := int(c.config.MSS())
	written := 0
	for len(b) > 0 {
		chunk := b
		if len(chunk) > mss {
			chunk = chunk[:mss]
		}
		if err := c.writeChunk(chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		b = b[len(chunk):]
	}
	return written, nil
}

// writeChunk 把一个不超过 MSS 的分片塞进发送窗口，必要时等待窗口腾出位置。
func (c *Connection) writeChunk(chunk []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if c.state.closedForWrite() {
			return ErrConnClosed
		}
		// 发送缓冲满了就等——不等的话上层能无限塞，内存直接起飞。
		if uint32(c.sending.Len()) < c.config.SendingWindowSize {
			seg := &DataSegment{
				Conv:   c.meta.Conversation,
				Number: c.nextNumber,
				Data:   append([]byte(nil), chunk...),
			}
			c.nextNumber++
			c.sending.Push(seg)
			c.flushLocked()
			return nil
		}
		if err := c.waitLocked(c.writeDeadline); err != nil {
			return err
		}
	}
}

// waitLocked 等到有人 Broadcast 或者截止时间到。调用方须持锁。
//
// sync.Cond 不支持超时，所以用一个一次性的定时器在到点时叫醒所有等待者。
// 唤醒后由各自的循环条件决定是继续等还是返回超时。
func (c *Connection) waitLocked(deadline time.Time) error {
	if !deadline.IsZero() {
		if !time.Now().Before(deadline) {
			return ErrTimeout
		}
		timer := time.AfterFunc(time.Until(deadline), func() {
			c.mu.Lock()
			c.cond.Broadcast()
			c.mu.Unlock()
		})
		defer timer.Stop()
	}
	c.cond.Wait()
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return ErrTimeout
	}
	return nil
}

func (c *Connection) Close() error {
	c.mu.Lock()
	next, changed := nextOnLocalClose(c.state)
	if changed {
		c.setState(next)
		// 立刻把关闭意图发出去，不等下一个 tick——对端早一点知道，
		// 就能早一点释放它那侧的会话。
		c.pingLocked(c.Elapsed(), CommandPing)
		c.flushOutput()
	}
	c.cond.Broadcast()
	terminated := c.state == StateTerminated
	c.mu.Unlock()

	if terminated {
		c.terminate()
	}
	if !changed {
		return ErrConnClosed
	}
	return nil
}

// terminate 释放资源。只会真正执行一次。
func (c *Connection) terminate() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.closer != nil {
			_ = c.closer.Close()
		}
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
}

func (c *Connection) LocalAddr() net.Addr  { return c.meta.LocalAddr }
func (c *Connection) RemoteAddr() net.Addr { return c.meta.RemoteAddr }

func (c *Connection) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline, c.writeDeadline = t, t
	c.cond.Broadcast()
	return nil
}

func (c *Connection) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	c.cond.Broadcast()
	return nil
}

func (c *Connection) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeDeadline = t
	c.cond.Broadcast()
	return nil
}

//------------------------------------------------------------------------------
// 收包
//------------------------------------------------------------------------------

// Input 处理一批从网络上收到的段。由监听器或拨号器的收包循环调用。
func (c *Connection) Input(segments []Segment) {
	current := c.Elapsed()
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastIncoming = current
	for _, s := range segments {
		// 会话号对不上的段直接丢：多半是上一条连接的残留包打到了新会话，
		// 处理它会污染窗口状态。
		if s.Conversation() != c.meta.Conversation {
			continue
		}
		switch seg := s.(type) {
		case *DataSegment:
			c.handleDataLocked(seg)
		case *AckSegment:
			c.handleAckLocked(current, seg)
		case *CmdOnlySegment:
			c.handleCmdLocked(current, seg)
		}
	}
	c.cond.Broadcast()
}

func (c *Connection) handleDataLocked(seg *DataSegment) {
	// 已交付过的重传也要回 ACK：对端就是因为没收到上次的 ACK 才重传的，
	// 沉默会让它一直重传下去。
	if c.receiving.IsStale(seg.Number) {
		c.acks.Add(seg.Number, seg.Timestamp)
		return
	}
	if !c.receiving.Accept(seg) {
		// 超出窗口：不回 ACK。回了等于承认收下一段我们其实丢弃的数据，
		// 对端就不会重传它了。
		return
	}
	c.acks.Add(seg.Number, seg.Timestamp)
	// 对端的 SendingNext 说明它发到哪了，据此清掉不必再确认的 ACK。
	c.acks.Clear(seg.SendingNext)
	for _, delivered := range c.receiving.Drain() {
		c.readBuf = append(c.readBuf, delivered.Data...)
	}
}

func (c *Connection) handleAckLocked(current timestamp, seg *AckSegment) {
	// 累积确认先走，它一次能清掉一片。
	c.sending.Clear(seg.ReceivingNext)
	c.peerReceivingNext = seg.ReceivingNext + seg.ReceivingWindow
	for _, number := range seg.NumberList {
		if c.sending.Remove(number) {
			// 只有确实在窗口里的段才贡献 RTT 样本。已经被累积确认清掉的
			// 段再算一次会把 RTT 拉长——那个时间戳是它首发时的。
			if timeAfter(current, seg.Timestamp) {
				c.roundTrip.Update(current-seg.Timestamp, current)
			}
		}
	}
	if len(seg.NumberList) > 0 {
		// 收到了更大的序号，比它小的多半丢了，把它们的重传提前。
		last := seg.NumberList[len(seg.NumberList)-1]
		c.sending.HandleFastAck(last, c.roundTrip.Timeout())
	}
}

func (c *Connection) handleCmdLocked(current timestamp, seg *CmdOnlySegment) {
	c.sending.Clear(seg.ReceivingNext)
	c.acks.Clear(seg.SendingNext)
	c.roundTrip.UpdatePeerRTO(seg.PeerRTO, current)

	if seg.Cmd == CommandTerminate {
		if next, changed := nextOnPeerTerminate(c.state); changed {
			c.setState(next)
		}
		return
	}
	if seg.Option == OptionClose {
		if next, changed := nextOnPeerClose(c.state); changed {
			c.setState(next)
		}
	}
}

//------------------------------------------------------------------------------
// 时间驱动
//------------------------------------------------------------------------------

func (c *Connection) updateLoop() {
	ticker := time.NewTicker(c.config.Tick)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			c.flushLocked()
			terminated := c.state == StateTerminated
			c.mu.Unlock()
			if terminated {
				c.terminate()
				return
			}
		}
	}
}

// flushLocked 推进一次：状态兜底、回 ACK、重传、心跳。调用方须持锁。
func (c *Connection) flushLocked() {
	current := c.Elapsed()
	if c.state == StateTerminated {
		return
	}

	// 状态兜底。顺序有讲究：先处理「能不能往下走」，再处理「等太久了」。
	switch c.state {
	case StateActive:
		if current-c.lastIncoming >= idleTimeout {
			// 对端消失了。直接进 Terminating 而不是 ReadyToClose——
			// 都联系不上了，没必要再等着把数据发完。
			c.setState(StateTerminating)
		}
	case StateReadyToClose:
		if c.sending.IsEmpty() {
			c.setState(StateTerminating)
		} else if current-c.stateSince > readyToCloseTimeout {
			c.setState(StateTerminating)
		}
	case StatePeerTerminating:
		if current-c.stateSince > peerTerminatingTimeout {
			c.setState(StateTerminating)
		}
	}

	if c.state == StateTerminating {
		c.pingLocked(current, CommandTerminate)
		if current-c.stateSince > terminatingTimeout {
			c.setState(StateTerminated)
		}
		c.flushOutput()
		return
	}

	c.acks.Flush(current, c.roundTrip.Timeout(), c.meta.Conversation,
		c.receiving.NextNumber(), c.receiving.Remaining())

	// 在途额度取三者最小：拥塞窗口、对端还能收多少、配置上限。
	// 少算任何一个都会把对端淹掉或者自己憋死。
	inFlight := c.cwnd.Size()
	if room := c.peerReceivingNext - c.sending.FirstNumber(); c.sending.Len() > 0 && room < inFlight {
		inFlight = room
	}
	c.sending.Flush(current, c.roundTrip.Timeout(), inFlight)

	if current-c.lastPing >= pingInterval {
		c.pingLocked(current, CommandPing)
	}
	c.flushOutput()
}

// flushOutput 告诉底层写入方这一批段结束了。
//
// 这一轮攒的 ACK、数据、心跳会被打进同一个 UDP 包。不调的话它们会一直
// 留在缓冲里，直到下一批把它挤满才发出去——延迟会变得很难看。
func (c *Connection) flushOutput() {
	if f, ok := c.output.(flusher); ok {
		_ = f.Flush()
	}
}

// pingLocked 发一个命令段。它同时承担三件事：保活、告知我们的接收进度、
// 交换 RTO。调用方须持锁。
func (c *Connection) pingLocked(current timestamp, cmd Command) {
	seg := &CmdOnlySegment{
		Conv:          c.meta.Conversation,
		Cmd:           cmd,
		SendingNext:   c.sending.FirstNumber(),
		ReceivingNext: c.receiving.NextNumber(),
		PeerRTO:       c.roundTrip.Timeout(),
	}
	if c.state == StateReadyToClose {
		seg.Option = OptionClose
	}
	_ = c.output.Write(seg)
	c.lastPing = current
}
