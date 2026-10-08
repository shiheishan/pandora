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
		return fmt.Errorf("xhttp packet queue is full")
	}
	q.storeLocked(packet)
	return nil
}

func (q *XHTTPPacketQueue) storeLocked(packet XHTTPPacket) {
	q.pending[packet.Seq] = append([]byte(nil), packet.Payload...)
	if packet.Seq >= q.tail {
		q.tail = packet.Seq + 1
	}
	q.signalLocked()
}

// PushWait 与 Push 相同，但队列满时等读端腾出位置而不是报错。
//
// 本端产生的流（下行、stream-up 的上行）必须靠它限速：Push 在满时直接报错，
// 下载稍快于客户端取走的速度，连接就被「队列已满」打断。
func (q *XHTTPPacketQueue) PushWait(ctx context.Context, packet XHTTPPacket) error {
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
		if packet.Seq < q.next {
			q.mu.Unlock()
			return nil
		}
		if _, ok := q.pending[packet.Seq]; ok {
			q.mu.Unlock()
			return fmt.Errorf("xhttp packet sequence %d already queued", packet.Seq)
		}
		if len(q.pending) < q.maxPending {
			q.storeLocked(packet)
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

// AppendWait 把一段负载按下一个序号排进队列（stream-up 的流式上行没有线上序号），
// 队列满时等待。
func (q *XHTTPPacketQueue) AppendWait(ctx context.Context, payload []byte) error {
	if q == nil {
		return fmt.Errorf("xhttp packet queue is nil")
	}
	q.mu.Lock()
	seq := q.tail
	if seq < q.next {
		seq = q.next
	}
	q.tail = seq + 1
	q.mu.Unlock()
	return q.PushWait(ctx, XHTTPPacket{Seq: seq, Payload: payload})
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
	for pendingSeq := range q.pending {
		if pendingSeq < seq {
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
	uplink, err := NewXHTTPPacketQueue(b.maxPending)
	if err != nil {
		return nil, err
	}
	downlink, err := NewXHTTPPacketQueue(b.maxPending)
	if err != nil {
		return nil, err
	}
	b.sessions[id] = &xhttpPacketSession{uplink: uplink, downlink: downlink, lastSeen: time.Now()}
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

func (b *XHTTPPacketBroker) Close(id string, err error) error {
	b.mu.Lock()
	session := b.sessions[id]
	delete(b.sessions, id)
	b.mu.Unlock()
	if session != nil {
		_ = session.uplink.Close(err)
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
		}
	}
	b.mu.Unlock()
	for _, duplex := range expired {
		_ = duplex.Uplink.Close(fmt.Errorf("xhttp packet session expired"))
		_ = duplex.Downlink.Close(fmt.Errorf("xhttp packet session expired"))
	}
	return len(expired)
}
