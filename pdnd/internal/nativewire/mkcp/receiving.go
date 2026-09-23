package mkcp

// 接收窗口：把乱序到达的段拼回有序的字节流，并回 ACK。
//
// 与发送侧对称，但简单得多——没有拥塞控制要配合，只有两件事：
// 缓存乱序段直到空洞被填上，以及告诉对端「我收到了哪些」。

// ReceivingWindow 缓存乱序到达的段，按序交付。
//
// 用 map 而不是环形数组：窗口容量由对端的发送速率决定，可以配到几千，
// 而实际乱序通常只差几个段。map 只为真正到达的段付出内存。
type ReceivingWindow struct {
	cache map[uint32]*DataSegment
	// nextNumber 是下一个要交付给上层的序号。它同时是回给对端的
	// ReceivingNext——「这个序号之前我都收齐了」。
	nextNumber uint32
	// capacity 是能缓存多少个乱序段。超出就丢：对端迟早会重传，而
	// 无上限缓存会被一个远远超前的段撑爆内存。
	capacity uint32
}

func NewReceivingWindow(capacity uint32) *ReceivingWindow {
	if capacity == 0 {
		capacity = 128
	}
	return &ReceivingWindow{cache: make(map[uint32]*DataSegment), capacity: capacity}
}

// NextNumber 是期待的下一个序号，也是回给对端的 ReceivingNext。
func (w *ReceivingWindow) NextNumber() uint32 { return w.nextNumber }

// Accept 收下一个段。
//
// 三种结果：接受（true）、重复或过期（false）、超出窗口（false）。
// 后两种都返回 false 但含义不同——过期的段仍要回 ACK（对端没收到上次的
// ACK 才会重传），超窗的段则连 ACK 都不该回，回了等于承认收到。
// 这个区别由调用方根据 InWindow 判断。
func (w *ReceivingWindow) Accept(seg *DataSegment) bool {
	if !w.InWindow(seg.Number) {
		return false
	}
	if _, exists := w.cache[seg.Number]; exists {
		return false
	}
	w.cache[seg.Number] = seg
	return true
}

// InWindow 判断序号是否落在当前接收窗口内。
//
// 早于 nextNumber 的是重传（已经交付过），晚于窗口上界的是对端跑太快。
func (w *ReceivingWindow) InWindow(number uint32) bool {
	if !numberAfter(number, w.nextNumber) && number != w.nextNumber {
		return false // 早于窗口，已经交付过了
	}
	return number-w.nextNumber < w.capacity
}

// IsStale 判断序号是否早于窗口（已交付过的重传）。
// 这类段要回 ACK，否则对端会一直重传下去。
func (w *ReceivingWindow) IsStale(number uint32) bool {
	return !numberAfter(number, w.nextNumber) && number != w.nextNumber
}

// Drain 取出所有连续可交付的段，并推进 nextNumber。
//
// 一次取干净而不是一次取一个：空洞被填上时往往能一口气交付好几个，
// 逐个调用会让上层反复进出锁。
func (w *ReceivingWindow) Drain() []*DataSegment {
	var out []*DataSegment
	for {
		seg, ok := w.cache[w.nextNumber]
		if !ok {
			break
		}
		delete(w.cache, w.nextNumber)
		out = append(out, seg)
		w.nextNumber++
	}
	return out
}

// Buffered 是当前缓存的乱序段数量。
func (w *ReceivingWindow) Buffered() int { return len(w.cache) }

//------------------------------------------------------------------------------
// ACK 列表
//------------------------------------------------------------------------------

// AckList 攒着待回复的 ACK，按 MSS 打包成 AckSegment 发出。
//
// 不是收一个回一个：ACK 本身也占带宽，攒起来批量发能省下大量小包。
// 代价是对端要多等一点，所以每个 ACK 有自己的重发节奏（见 Flush）。
type AckList struct {
	writer SegmentWriter
	// 三个切片并行索引同一个 ACK。用并行切片而不是结构体切片：
	// Flush 里只读 numbers 和 nextFlush，分开存对缓存更友好。
	numbers    []uint32
	timestamps []uint32
	// nextFlush 是每个 ACK 的下次重发时刻。0 表示还没发过。
	nextFlush []timestamp
	// dirty 表示有新加入、尚未发出的 ACK。没有新东西时不该发空段。
	dirty bool
	// perSegment 是一个 AckSegment 能装多少个序号，由 MSS 决定。
	perSegment int
}

func NewAckList(writer SegmentWriter, mss uint32) *AckList {
	// ACK 段固定开销 17 字节，剩下的空间每个序号占 4 字节。
	perSegment := (int(mss) - 17) / 4
	if perSegment < 1 {
		perSegment = 1
	}
	if perSegment > ackNumberLimit {
		perSegment = ackNumberLimit
	}
	return &AckList{writer: writer, perSegment: perSegment}
}

func (l *AckList) Len() int { return len(l.numbers) }

// Add 记下一个要确认的序号。timestamp 是对端发来的原值，原样回显，
// 对端拿它算 RTT——不能用我们自己的时钟，两边不同步。
func (l *AckList) Add(number uint32, timestamp uint32) {
	l.numbers = append(l.numbers, number)
	l.timestamps = append(l.timestamps, timestamp)
	l.nextFlush = append(l.nextFlush, 0)
	l.dirty = true
}

// Clear 丢掉序号小于 una 的 ACK。
//
// una 来自对端的 SendingNext 推进：它已经知道我们收到了，不用再确认。
func (l *AckList) Clear(una uint32) {
	kept := 0
	for i := range l.numbers {
		// 回绕安全：直接写 numbers[i] < una 在回绕点会把该留的丢掉，
		// 表现是对端反复重传一个我们其实收到了的段。
		if numberAfter(l.numbers[i], una) || l.numbers[i] == una {
			if i != kept {
				l.numbers[kept] = l.numbers[i]
				l.timestamps[kept] = l.timestamps[i]
				l.nextFlush[kept] = l.nextFlush[i]
			}
			kept++
		}
	}
	if kept < len(l.numbers) {
		l.numbers = l.numbers[:kept]
		l.timestamps = l.timestamps[:kept]
		l.nextFlush = l.nextFlush[:kept]
		l.dirty = true
	}
}

// Flush 把到期的 ACK 打包发出，返回发出的段数。
//
// 每个 ACK 发出后要等 rto/2 才会再发一次（下限 20ms）。为什么要重发：
// ACK 丢了对端会重传数据，而重传的数据我们已经有了，白白浪费一个 RTT。
// 不无限重发是因为 Clear 会在对端推进后把它们清掉。
// receivingWindow 是接收侧还能容纳多少个段。这个值必须如实告诉对端：
// 它拿 ReceivingNext + ReceivingWindow 算自己还能发多少。填 0 的后果是
// 对端算出的额度为 0，一个段都发不出来——丢包时尤其致命，因为
// ReceivingNext 卡住不动，重传也跟着发不出去，连接直接死锁。
func (l *AckList) Flush(current timestamp, rto uint32, conv uint16,
	receivingNext, receivingWindow uint32) int {
	if len(l.numbers) == 0 && !l.dirty {
		return 0
	}

	timeout := rto / 2
	if timeout < 20 {
		timeout = 20
	}

	seg := &AckSegment{Conv: conv, ReceivingNext: receivingNext, ReceivingWindow: receivingWindow}
	sent := 0
	flush := func() {
		if len(seg.NumberList) == 0 && !l.dirty {
			return
		}
		if l.writer.Write(seg) == nil {
			sent++
		}
		l.dirty = false
		seg = &AckSegment{Conv: conv, ReceivingNext: receivingNext}
	}

	for i := range l.numbers {
		if l.nextFlush[i] != 0 && !timeAfter(current, l.nextFlush[i]) {
			continue // 还没到重发时刻
		}
		seg.NumberList = append(seg.NumberList, l.numbers[i])
		// 时间戳取这一批里最新的：对端只用它算一个 RTT 样本，给最新的
		// 那个最接近真实往返时间。
		if timeAfter(l.timestamps[i], seg.Timestamp) {
			seg.Timestamp = l.timestamps[i]
		}
		l.nextFlush[i] = current + timeout
		if len(seg.NumberList) >= l.perSegment {
			flush()
		}
	}
	flush()
	return sent
}

// Remaining 是还能容纳多少个乱序段。
//
// 这个值会放进 ACK 告诉对端「你还能发多少」。报大了对端会淹掉我们，
// 报小了它会憋着不发——报 0 更是直接让它一个段都发不出来。
func (w *ReceivingWindow) Remaining() uint32 {
	used := uint32(len(w.cache))
	if used >= w.capacity {
		return 0
	}
	return w.capacity - used
}
