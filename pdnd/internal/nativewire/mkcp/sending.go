package mkcp

// 发送窗口：管住「已发出但还没被确认」的那些段。
//
// 职责有三个：按序号保存待确认的段、在超时后重发、根据对端的 ACK 把已经
// 送达的段丢掉。拥塞控制不在这里——那一层决定「一次能在途多少个」，窗口
// 只负责在给定的额度内挑出该发的段。

// timestamp 是 mKCP 里的时间，单位毫秒，取自连接建立后的相对时刻。
//
// 它是 uint32，约 49.7 天回绕一次。所有比较都必须用差值而不是直接大小
// 比较——回绕之后 current < timeout 会让「已超时」被判成「还早着呢」，
// 表现是连接卡死而不是重发。
type timestamp = uint32

// timeAfter 判断 a 是否在 b 之后（考虑回绕）。
//
// 差值落在后半区（>= 0x7FFFFFFF）说明 a 其实在 b 之前，只是减法回绕了。
// 这是 mKCP/TCP 序号比较的通用技巧，xray 里写成
// `current-timeout >= 0x7FFFFFFF` 的地方就是这个意思。
func timeAfter(a, b timestamp) bool {
	return a != b && a-b < 0x7FFFFFFF
}

// numberAfter 判断序号 a 是否在 b 之后。序号和时间戳一样是 uint32 回绕。
func numberAfter(a, b uint32) bool {
	return a != b && a-b < 0x7FFFFFFF
}

// SegmentWriter 把段发出去。窗口不关心底下是 UDP 还是别的什么。
type SegmentWriter interface {
	Write(seg Segment) error
}

// sendingEntry 是窗口里的一个待确认段。
type sendingEntry struct {
	seg *DataSegment
	// timeout 是该段的下次重发时刻。
	timeout timestamp
	// transmit 是已发送次数。0 表示还没发过——首发和重发在丢包统计里
	// 意义不同，混在一起算会把正常的首发也算成丢包。
	transmit uint32
}

// SendingWindow 保存未确认的段。非并发安全，由调用方加锁。
type SendingWindow struct {
	// 按序号递增排列。用切片而不是链表：删除集中在头部（累积确认），
	// 中间删除本来就要先遍历定位，链表省不掉那一趟。
	entries []sendingEntry
	// totalInFlightSize 是首发过、尚未确认的段数，丢包率的分母。
	totalInFlightSize uint32
	writer            SegmentWriter
	// onPacketLoss 汇报一次 Flush 里的重发占比（0-100）。拥塞控制据此
	// 收缩窗口。可以为 nil。
	onPacketLoss func(rate uint32)
}

func NewSendingWindow(writer SegmentWriter, onPacketLoss func(uint32)) *SendingWindow {
	return &SendingWindow{writer: writer, onPacketLoss: onPacketLoss}
}

func (w *SendingWindow) Len() int      { return len(w.entries) }
func (w *SendingWindow) IsEmpty() bool { return len(w.entries) == 0 }

// FirstNumber 返回窗口里最小的序号。空窗口调用是调用方的错，返回 0。
func (w *SendingWindow) FirstNumber() uint32 {
	if len(w.entries) == 0 {
		return 0
	}
	return w.entries[0].seg.Number
}

// Push 追加一个待发送的段。调用方保证序号递增。
func (w *SendingWindow) Push(seg *DataSegment) {
	w.entries = append(w.entries, sendingEntry{seg: seg})
}

// Clear 处理累积确认：丢掉所有序号小于 una 的段。
//
// una 是对端的 ReceivingNext，意思是「这个序号之前的我都收齐了」。
func (w *SendingWindow) Clear(una uint32) {
	cut := 0
	for cut < len(w.entries) && !numberAfter(w.entries[cut].seg.Number, una) &&
		w.entries[cut].seg.Number != una {
		if w.entries[cut].transmit > 0 && w.totalInFlightSize > 0 {
			w.totalInFlightSize--
		}
		cut++
	}
	if cut > 0 {
		w.entries = w.dropFront(cut)
	}
}

// Remove 处理选择确认：丢掉指定序号的那个段。返回是否真的删掉了。
func (w *SendingWindow) Remove(number uint32) bool {
	for i := range w.entries {
		n := w.entries[i].seg.Number
		if numberAfter(n, number) {
			// 已经越过目标序号，后面只会更大。
			return false
		}
		if n == number {
			if w.entries[i].transmit > 0 && w.totalInFlightSize > 0 {
				w.totalInFlightSize--
			}
			w.entries = w.dropAt(i)
			return true
		}
	}
	return false
}

// dropFront 丢掉前 n 个元素，并把腾出来的尾部清零。
//
// 只写 append(entries[:0], entries[n:]...) 是不够的：元素往前挪之后，
// 底层数组尾部还留着挪动前的副本，那些指针指向已经确认过的段，长连接下
// 会一直不被回收。切片长度看不出这个问题，得清零才行。
func (w *SendingWindow) dropFront(n int) []sendingEntry {
	kept := copy(w.entries, w.entries[n:])
	w.clearTail(kept)
	return w.entries[:kept]
}

// dropAt 丢掉下标 i 的元素，同样清零尾部。
func (w *SendingWindow) dropAt(i int) []sendingEntry {
	kept := copy(w.entries[i:], w.entries[i+1:]) + i
	w.clearTail(kept)
	return w.entries[:kept]
}

// clearTail 把 [kept, len) 区间置零，断开对已移除段的引用。
func (w *SendingWindow) clearTail(kept int) {
	for i := kept; i < len(w.entries); i++ {
		w.entries[i] = sendingEntry{}
	}
}

// HandleFastAck 处理快速重传。
//
// 对端确认了序号 number，说明比它小的那些要么也到了、要么丢了。与其等它们
// 各自的超时，不如把超时往前挪 rto/3——链路正常时这能省掉一整个 RTO 的等待。
//
// 只对已经发过的段生效：还没发出去的段没有「重传」可言。
func (w *SendingWindow) HandleFastAck(number uint32, rto uint32) {
	for i := range w.entries {
		n := w.entries[i].seg.Number
		if n == number || numberAfter(n, number) {
			break
		}
		e := &w.entries[i]
		if e.transmit > 0 && e.timeout > rto/3 {
			e.timeout -= rto / 3
		}
	}
}

// Flush 把到期的段发出去，最多发 maxInFlight 个。
//
// 返回实际写出的段数。写失败会中断本轮——底层多半是 UDP 缓冲满了，
// 接着写只是徒劳，等下一轮再说。
func (w *SendingWindow) Flush(current timestamp, rto uint32, maxInFlight uint32) int {
	if len(w.entries) == 0 || maxInFlight == 0 {
		return 0
	}

	var lost, sent uint32
	for i := range w.entries {
		if sent >= maxInFlight {
			break
		}
		e := &w.entries[i]
		// 还没到重发时刻就跳过。注意这里必须用回绕安全的比较：时间戳
		// 是 uint32，直接 current < e.timeout 在回绕点附近会判反，
		// 后果是整个窗口被当成已超时反复重发。
		if e.transmit > 0 && !timeAfter(current, e.timeout) {
			continue
		}
		if e.transmit == 0 {
			w.totalInFlightSize++
		} else {
			lost++
		}
		e.timeout = current + rto
		e.transmit++
		e.seg.Timestamp = current
		if err := w.writer.Write(e.seg); err != nil {
			break
		}
		sent++
	}

	// 丢包率只在真发了东西时汇报，否则分子分母都是 0，会给拥塞控制
	// 一个「丢包率 0%」的假信号。
	if w.onPacketLoss != nil && sent > 0 && w.totalInFlightSize > 0 {
		w.onPacketLoss(lost * 100 / w.totalInFlightSize)
	}
	return int(sent)
}

// Visit 按序遍历窗口。visitor 返回 false 就停下。
func (w *SendingWindow) Visit(visitor func(seg *DataSegment) bool) {
	for i := range w.entries {
		if !visitor(w.entries[i].seg) {
			return
		}
	}
}
