package kernel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// XHTTPPacket is one ordered uplink/downlink unit. Sequence numbers are
// monotonic per XHTTP session and start at zero, matching the wire clients.
type XHTTPPacket struct {
	Seq     uint64
	Payload []byte
}

// XHTTPPacketQueue is the bounded scheduling primitive used by packet-up and
// reconnecting XHTTP sessions. Push is idempotent for a retransmitted
// sequence, while Read returns packets strictly in sequence order.
type XHTTPPacketQueue struct {
	mu         sync.Mutex
	next       uint64
	pending    map[uint64][]byte
	maxPending int
	notify     chan struct{}
	closed     bool
	closeErr   error
	// tail 是下一个由 AppendWait 分配的序号（已收到的最大序号 + 1）。
	tail uint64
	// waiters 是在 PushWait 里等位置的写方数；Read 腾出位置时只在有人等才唤醒。
	waiters int
	// bytes 是待读负载的总字节数。budget 非空时（会话还没认证）它同时计入入站
	// 共享预算，且不得超过 byteLimit——未认证客户端靠乱序包钉内存的上限，见
	// xhttp_budget.go。
	bytes     int64
	budget    *xhttpByteBudget
	byteLimit int64
}

func NewXHTTPPacketQueue(maxPending int) (*XHTTPPacketQueue, error) {
	if maxPending <= 0 {
		return nil, fmt.Errorf("xhttp packet queue max_pending must be positive")
	}
	return &XHTTPPacketQueue{pending: make(map[uint64][]byte), maxPending: maxPending, notify: make(chan struct{})}, nil
}

func (q *XHTTPPacketQueue) Push(packet XHTTPPacket) error {
	if q == nil {
		return fmt.Errorf("xhttp packet queue is nil")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return q.closeErrOrDefault()
	}
	if packet.Seq < q.next {
		// A client retry after the packet has been consumed is harmless.
		return nil
	}
	if existing, ok := q.pending[packet.Seq]; ok {
		if string(existing) != string(packet.Payload) {
			return fmt.Errorf("xhttp packet sequence %d payload conflict", packet.Seq)
		}
		return nil
	}
	if len(q.pending) >= q.maxPending {
		return fmt.Errorf("%w: xhttp packet queue is full", errXHTTPCapacity)
	}
	if !q.chargeLocked(len(packet.Payload)) {
		return fmt.Errorf("%w: xhttp unauthenticated session over budget", errXHTTPCapacity)
	}
	q.storeLocked(packet)
	return nil
}

// chargeLocked 为一段待入队负载记账；会话未认证且超出单会话上限或入站共享预算
// 时返回 false。
func (q *XHTTPPacketQueue) chargeLocked(n int) bool {
	if q.budget != nil {
		if q.bytes+int64(n) > q.byteLimit || !q.budget.take(int64(n)) {
			return false
		}
	}
	q.bytes += int64(n)
	return true
}

// releaseLocked 归还已读出（或丢弃）的负载字节。
func (q *XHTTPPacketQueue) releaseLocked(n int) {
	q.bytes -= int64(n)
	if q.budget != nil {
		q.budget.give(int64(n))
	}
}

func (q *XHTTPPacketQueue) storeLocked(packet XHTTPPacket) {
	q.pending[packet.Seq] = append([]byte(nil), packet.Payload...)
	if packet.Seq >= q.tail {
		q.tail = packet.Seq + 1
	}
	q.signalLocked()
}

// useBudget 让这个队列的待读字节计入共享预算（会话建立、尚未认证时）。
func (q *XHTTPPacketQueue) useBudget(budget *xhttpByteBudget, byteLimit int64) {
	q.mu.Lock()
	q.budget, q.byteLimit = budget, byteLimit
	q.mu.Unlock()
}

// leaveBudget 在会话认证后调用：已记的字节还给共享预算，此后不再受未认证上限约束。
func (q *XHTTPPacketQueue) leaveBudget() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.budget != nil {
		q.budget.give(q.bytes)
		q.budget = nil
	}
	q.signalLocked()
}

// discard 在会话结束、不会再有人读时丢掉全部待读负载并归还预算。
func (q *XHTTPPacketQueue) discard() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for seq, payload := range q.pending {
		q.releaseLocked(len(payload))
		delete(q.pending, seq)
	}
	if q.budget != nil {
		q.budget = nil
	}
}

// PushWait 与 Push 相同，但队列满时等读端腾出位置而不是报错。
//
// 本端产生的流（下行、stream-up 的上行）必须靠它限速：Push 在满时直接报错，
// 下载稍快于客户端取走的速度，连接就被「队列已满」打断。
func (q *XHTTPPacketQueue) PushWait(ctx context.Context, packet XHTTPPacket) error {
	return q.waitStore(ctx, packet.Payload, func() (uint64, bool) { return packet.Seq, true })
}

// AppendWait 把一段负载按下一个序号排进队列（stream-up 的流式上行没有线上序号），
// 队列满时等待。序号在确定能入队的那一刻才分配：等待中被取消不会留下空洞。
func (q *XHTTPPacketQueue) AppendWait(ctx context.Context, payload []byte) error {
	return q.waitStore(ctx, payload, func() (uint64, bool) {
		seq := q.tail
		if seq < q.next {
			seq = q.next
		}
		return seq, false
	})
}

// waitStore 等到有位置（且未认证会话的字节预算够）再入队。seqOf 在锁内调用；
// fixed 为真表示序号来自线上（重复、过期的序号按 Push 的规则处理）。
func (q *XHTTPPacketQueue) waitStore(ctx context.Context, payload []byte, seqOf func() (uint64, bool)) error {
	if q == nil {
		return fmt.Errorf("xhttp packet queue is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	for {
		if q.closed {
			err := q.closeErrOrDefault()
			q.mu.Unlock()
			return err
		}
		seq, fixed := seqOf()
		if fixed {
			if seq < q.next {
				q.mu.Unlock()
				return nil
			}
			if _, ok := q.pending[seq]; ok {
				q.mu.Unlock()
				return fmt.Errorf("xhttp packet sequence %d already queued", seq)
			}
		}
		if len(q.pending) < q.maxPending && q.chargeLocked(len(payload)) {
			q.storeLocked(XHTTPPacket{Seq: seq, Payload: payload})
			q.mu.Unlock()
			return nil
		}
		notify := q.notify
		q.waiters++
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			q.mu.Lock()
			q.waiters--
			q.mu.Unlock()
			return ctx.Err()
		case <-notify:
		}
		q.mu.Lock()
		q.waiters--
	}
}

func (q *XHTTPPacketQueue) Read(ctx context.Context) (XHTTPPacket, error) {
	if q == nil {
		return XHTTPPacket{}, fmt.Errorf("xhttp packet queue is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		q.mu.Lock()
		if payload, ok := q.pending[q.next]; ok {
			packet := XHTTPPacket{Seq: q.next, Payload: append([]byte(nil), payload...)}
			delete(q.pending, q.next)
			q.next++
			q.releaseLocked(len(payload))
			if q.waiters > 0 {
				q.signalLocked()
			}
			q.mu.Unlock()
			return packet, nil
		}
		if q.closed {
			err := q.closeErrOrDefault()
			q.mu.Unlock()
			return XHTTPPacket{}, err
		}
		notify := q.notify
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return XHTTPPacket{}, ctx.Err()
		case <-notify:
		}
	}
}

// ResumeFrom drops packets below the acknowledged sequence and makes that
// sequence the next packet to read. It is used when a reconnect carries an
// explicit receive checkpoint.
func (q *XHTTPPacketQueue) ResumeFrom(seq uint64) error {
	if q == nil {
		return fmt.Errorf("xhttp packet queue is nil")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return q.closeErrOrDefault()
	}
	if seq < q.next {
		return fmt.Errorf("xhttp resume sequence %d is behind %d", seq, q.next)
	}
	for pendingSeq, payload := range q.pending {
		if pendingSeq < seq {
			q.releaseLocked(len(payload))
			delete(q.pending, pendingSeq)
		}
	}
	q.next = seq
	q.signalLocked()
	return nil
}

func (q *XHTTPPacketQueue) Expected() uint64 {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.next
}

func (q *XHTTPPacketQueue) Close(err error) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed, q.closeErr = true, err
	q.signalLocked()
	return nil
}

func (q *XHTTPPacketQueue) signalLocked() {
	close(q.notify)
	q.notify = make(chan struct{})
}

func (q *XHTTPPacketQueue) closeErrOrDefault() error {
	if q.closeErr != nil {
		return q.closeErr
	}
	return fmt.Errorf("xhttp packet queue closed")
}

type xhttpPacketSession struct {
	uplink   *XHTTPPacketQueue
	downlink *XHTTPPacketQueue
	lastSeen time.Time
	// unauth：会话还没通过协议层认证，计入 broker.unauth 与共享字节预算。
	unauth bool
}

type XHTTPPacketDuplex struct {
	Uplink   *XHTTPPacketQueue
	Downlink *XHTTPPacketQueue
}

// XHTTPPacketBroker keeps packet queues alive across independent HTTP
// requests, allowing a client to reconnect without losing ordered state.
// Endpoint parsing and VLESS tunnel wiring consume this broker in the next
// transport slice; the broker itself is protocol-agnostic and bounded.
type XHTTPPacketBroker struct {
	mu          sync.Mutex
	sessions    map[string]*xhttpPacketSession
	maxPending  int
	idleTimeout time.Duration
	// limits 为零值时不设上限（单测直接构造的 broker）；入站用的 broker 经
	// newXHTTPSessionBroker 设好，见 xhttp_budget.go。
	limits xhttpSessionLimits
	unauth int
}

func NewXHTTPPacketBroker(maxPending int, idleTimeout time.Duration) (*XHTTPPacketBroker, error) {
	if maxPending <= 0 || idleTimeout <= 0 {
		return nil, fmt.Errorf("xhttp packet broker limits must be positive")
	}
	return &XHTTPPacketBroker{sessions: make(map[string]*xhttpPacketSession), maxPending: maxPending, idleTimeout: idleTimeout}, nil
}

func (b *XHTTPPacketBroker) Open(id string) (*XHTTPPacketQueue, error) {
	duplex, err := b.OpenDuplex(id)
	if err != nil {
		return nil, err
	}
	return duplex.Uplink, nil
}

func (b *XHTTPPacketBroker) OpenDuplex(id string) (*XHTTPPacketDuplex, error) {
	if b == nil || id == "" {
		return nil, fmt.Errorf("xhttp packet session id is required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[id]; session != nil {
		session.lastSeen = time.Now()
		return &XHTTPPacketDuplex{Uplink: session.uplink, Downlink: session.downlink}, nil
	}
	if err := b.limits.admit(len(b.sessions), b.unauth); err != nil {
		return nil, err
	}
	uplink, err := NewXHTTPPacketQueue(b.maxPending)
	if err != nil {
		return nil, err
	}
	downlink, err := NewXHTTPPacketQueue(b.maxPending)
	if err != nil {
		return nil, err
	}
	session := &xhttpPacketSession{uplink: uplink, downlink: downlink, lastSeen: time.Now()}
	if b.limits.budget != nil {
		uplink.useBudget(b.limits.budget, b.limits.unauthSessionBytes)
		session.unauth = true
		b.unauth++
	}
	b.sessions[id] = session
	return &XHTTPPacketDuplex{Uplink: uplink, Downlink: downlink}, nil
}

func (b *XHTTPPacketBroker) Touch(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if session := b.sessions[id]; session != nil {
		session.lastSeen = time.Now()
		return nil
	}
	return fmt.Errorf("xhttp packet session %q not found", id)
}

// Close 结束并删除会话：关队列，丢掉上行待读负载（读上行的是会话 worker，它已经
// 退出），归还未认证预算。
func (b *XHTTPPacketBroker) Close(id string, err error) error {
	b.mu.Lock()
	session := b.sessions[id]
	delete(b.sessions, id)
	if session != nil && session.unauth {
		session.unauth = false
		b.unauth--
	}
	b.mu.Unlock()
	if session != nil {
		_ = session.uplink.Close(err)
		session.uplink.discard()
		// 下行不丢：下行 GET 还在把已排队的数据交给客户端，读完才结束。
		return session.downlink.Close(err)
	}
	return nil
}

func (b *XHTTPPacketBroker) GC(now time.Time) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	var expired []*XHTTPPacketDuplex
	for id, session := range b.sessions {
		if now.Sub(session.lastSeen) >= b.idleTimeout {
			expired = append(expired, &XHTTPPacketDuplex{Uplink: session.uplink, Downlink: session.downlink})
			delete(b.sessions, id)
			if session.unauth {
				b.unauth--
			}
		}
	}
	b.mu.Unlock()
	for _, duplex := range expired {
		_ = duplex.Uplink.Close(fmt.Errorf("xhttp packet session expired"))
		_ = duplex.Downlink.Close(fmt.Errorf("xhttp packet session expired"))
		duplex.Uplink.discard()
	}
	return len(expired)
}
