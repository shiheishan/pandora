package mkcp

import "testing"

func seg(n uint32, payload string) *DataSegment {
	return &DataSegment{Number: n, Data: []byte(payload)}
}

func drained(w *ReceivingWindow) string {
	out := ""
	for _, s := range w.Drain() {
		out += string(s.Data)
	}
	return out
}

// 顺序到达：来一个交付一个。
func TestReceivingWindowDeliversInOrder(t *testing.T) {
	w := NewReceivingWindow(64)
	for i, s := range []string{"a", "b", "c"} {
		if !w.Accept(seg(uint32(i), s)) {
			t.Fatalf("段 %d 应当被接受", i)
		}
	}
	if got := drained(w); got != "abc" {
		t.Fatalf("交付 %q，期望 abc", got)
	}
	if w.NextNumber() != 3 {
		t.Errorf("下一个序号 = %d，期望 3", w.NextNumber())
	}
}

// 乱序到达：空洞没填上之前一个都不交付，填上之后一口气交付。
func TestReceivingWindowHoldsUntilGapFilled(t *testing.T) {
	w := NewReceivingWindow(64)
	w.Accept(seg(1, "b"))
	w.Accept(seg(2, "c"))
	if got := drained(w); got != "" {
		t.Fatalf("0 号还没到就交付了 %q", got)
	}
	if w.Buffered() != 2 {
		t.Errorf("缓存 %d 个，期望 2 个", w.Buffered())
	}

	w.Accept(seg(0, "a"))
	if got := drained(w); got != "abc" {
		t.Fatalf("空洞填上后交付 %q，期望 abc", got)
	}
	if w.Buffered() != 0 {
		t.Errorf("交付后还缓存着 %d 个", w.Buffered())
	}
}

// 重复段不能重复交付——上层会看到重复的字节。
func TestReceivingWindowRejectsDuplicates(t *testing.T) {
	w := NewReceivingWindow(64)
	w.Accept(seg(0, "a"))
	if w.Accept(seg(0, "a")) {
		t.Error("同一序号第二次应当被拒")
	}
	if got := drained(w); got != "a" {
		t.Fatalf("交付 %q，期望 a", got)
	}
	// 交付之后再来一次（对端没收到 ACK 的重传），同样不能重复交付
	if w.Accept(seg(0, "a")) {
		t.Error("已交付过的序号不该被再次接受")
	}
	if got := drained(w); got != "" {
		t.Fatalf("重传导致重复交付 %q", got)
	}
}

// 已交付过的重传要能被识别出来——它们仍需回 ACK，否则对端一直重传。
func TestReceivingWindowMarksStaleSegments(t *testing.T) {
	w := NewReceivingWindow(64)
	w.Accept(seg(0, "a"))
	w.Drain() // nextNumber 推进到 1

	if !w.IsStale(0) {
		t.Error("已交付的序号应当被判为过期")
	}
	if w.IsStale(1) || w.IsStale(5) {
		t.Error("尚未交付的序号不该被判为过期")
	}
}

// 远远超前的段要丢掉：对端会重传，而无上限缓存能被一个段撑爆内存。
func TestReceivingWindowRejectsBeyondCapacity(t *testing.T) {
	w := NewReceivingWindow(8)
	if w.Accept(seg(100, "far")) {
		t.Error("超出窗口容量的段应当被拒")
	}
	if w.Buffered() != 0 {
		t.Errorf("被拒的段仍进了缓存：%d 个", w.Buffered())
	}
	// 边界：容量 8 时 0..7 可接受，8 不行
	if !w.InWindow(7) {
		t.Error("序号 7 应当在窗口内")
	}
	if w.InWindow(8) {
		t.Error("序号 8 超出容量 8 的窗口")
	}
}

// 序号回绕处，窗口判断不能失灵。
func TestReceivingWindowSurvivesWraparound(t *testing.T) {
	w := NewReceivingWindow(64)
	w.nextNumber = ^uint32(0) - 1 // 距回绕还有 2

	if !w.Accept(seg(w.nextNumber, "x")) {
		t.Fatal("当前期待的序号应当被接受")
	}
	if !w.Accept(seg(w.nextNumber+1, "y")) {
		t.Fatal("下一个序号应当被接受")
	}
	if !w.Accept(seg(w.nextNumber+2, "z")) { // 这个已经绕过 0
		t.Fatal("回绕后的序号应当被接受")
	}
	if got := drained(w); got != "xyz" {
		t.Fatalf("跨回绕交付 %q，期望 xyz", got)
	}
	if w.NextNumber() != 1 {
		t.Errorf("回绕后下一个序号 = %d，期望 1", w.NextNumber())
	}
}

//------------------------------------------------------------------------------
// ACK 列表
//------------------------------------------------------------------------------

type ackWriter struct {
	segs []*AckSegment
}

func (w *ackWriter) Write(s Segment) error {
	a := s.(*AckSegment)
	// 拷一份：调用方会复用段对象。字段要拷全——漏一个的话，那个字段
	// 上的 bug 在所有用这个 writer 的测试里都是隐形的。
	cp := &AckSegment{Conv: a.Conv, ReceivingNext: a.ReceivingNext,
		ReceivingWindow: a.ReceivingWindow, Timestamp: a.Timestamp}
	cp.NumberList = append(cp.NumberList, a.NumberList...)
	w.segs = append(w.segs, cp)
	return nil
}

func (w *ackWriter) allNumbers() []uint32 {
	var out []uint32
	for _, s := range w.segs {
		out = append(out, s.NumberList...)
	}
	return out
}

func TestAckListFlushesAddedNumbers(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	l.Add(1, 100)
	l.Add(2, 200)

	if n := l.Flush(1000, 200, 7, 3, 128); n != 1 {
		t.Fatalf("发出 %d 个段，期望 1 个", n)
	}
	if got := w.allNumbers(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("确认的序号 = %v，期望 [1 2]", got)
	}
	if w.segs[0].Conv != 7 || w.segs[0].ReceivingNext != 3 {
		t.Errorf("会话号/接收游标 = %d/%d，期望 7/3",
			w.segs[0].Conv, w.segs[0].ReceivingNext)
	}
	// 时间戳取最新的那个，对端拿它算最接近真实的 RTT
	if w.segs[0].Timestamp != 200 {
		t.Errorf("时间戳 = %d，期望 200", w.segs[0].Timestamp)
	}
}

// 刚发过的 ACK 不该立刻再发一遍，否则纯粹是浪费带宽。
func TestAckListRespectsResendInterval(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	l.Add(1, 100)
	l.Flush(1000, 200, 1, 2, 128) // 首发，下次 1100（rto/2=100）
	w.segs = nil

	if n := l.Flush(1050, 200, 1, 2, 128); n != 0 {
		t.Errorf("间隔内又发了 %d 个段", n)
	}
	if n := l.Flush(1101, 200, 1, 2, 128); n != 1 {
		t.Errorf("到点后应当重发，实际发了 %d 个", n)
	}
}

// 重发间隔有下限：rto 很小时不能退化成每次 Flush 都重发。
func TestAckListResendIntervalHasFloor(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	l.Add(1, 100)
	l.Flush(1000, 2, 1, 2, 128) // rto=2 → rto/2=1，但下限是 20
	w.segs = nil
	if n := l.Flush(1010, 2, 1, 2, 128); n != 0 {
		t.Error("下限内不该重发")
	}
	if n := l.Flush(1021, 2, 1, 2, 128); n != 1 {
		t.Error("过了下限应当重发")
	}
}

// 超过一个段能装的数量时要拆成多个段发。
func TestAckListSplitsBySegmentCapacity(t *testing.T) {
	w := &ackWriter{}
	// mss=41 → (41-17)/4 = 6 个序号一段
	l := NewAckList(w, 41)
	for i := uint32(0); i < 13; i++ {
		l.Add(i, i)
	}
	l.Flush(1000, 200, 1, 13, 128)

	if len(w.segs) != 3 {
		t.Fatalf("拆成 %d 个段，期望 3 个（6+6+1）", len(w.segs))
	}
	if got := w.allNumbers(); len(got) != 13 {
		t.Fatalf("确认了 %d 个序号，期望 13 个", len(got))
	}
	// 每段都不能超过容量，否则对端解析时会把多出来的当成下一个段
	for i, s := range w.segs {
		if len(s.NumberList) > 6 {
			t.Errorf("第 %d 段装了 %d 个序号，超过容量 6", i, len(s.NumberList))
		}
	}
}

// 单段容量不能超过协议上限（数量字段只有一个字节，xray 取 128）。
func TestAckListCapsAtProtocolLimit(t *testing.T) {
	l := NewAckList(&ackWriter{}, 65535)
	if l.perSegment > ackNumberLimit {
		t.Fatalf("单段容量 %d 超过协议上限 %d", l.perSegment, ackNumberLimit)
	}
}

func TestAckListClearDropsAcknowledged(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	for i := uint32(1); i <= 5; i++ {
		l.Add(i, i)
	}
	l.Clear(3) // 对端已推进到 3，1、2 不用再确认
	if l.Len() != 3 {
		t.Fatalf("剩余 %d 个，期望 3 个", l.Len())
	}
	l.Flush(1000, 200, 1, 6, 128)
	got := w.allNumbers()
	for _, n := range got {
		if n < 3 {
			t.Errorf("已被 Clear 的序号 %d 又发出去了", n)
		}
	}
}

// Clear 在序号回绕处不能把该留的丢掉——丢了会让对端反复重传。
func TestAckListClearSurvivesWraparound(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	nearMax := ^uint32(0) - 1
	l.Add(nearMax, 1)   // 回绕前
	l.Add(nearMax+1, 2) // 回绕前最后一个
	l.Add(nearMax+2, 3) // 溢出成 0
	l.Add(1, 4)

	l.Clear(nearMax + 2) // 确认到「溢出成 0」这个位置
	if l.Len() != 2 {
		t.Fatalf("回绕处 Clear 后剩 %d 个，期望 2 个", l.Len())
	}
	l.Flush(1000, 200, 1, 2, 128)
	got := w.allNumbers()
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("剩下的序号 = %v，期望 [0 1]", got)
	}
}

// 没有待确认的东西时不该发空段。
func TestAckListSkipsFlushWhenEmpty(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1400)
	if n := l.Flush(1000, 200, 1, 0, 128); n != 0 {
		t.Fatalf("空列表发出了 %d 个段", n)
	}
	if len(w.segs) != 0 {
		t.Errorf("空列表写出了 %d 个段", len(w.segs))
	}
}

// ACK 必须带上接收窗口余量。这条是回归守卫：漏填时它是 0，对端会算出
// 「还能发 0 个段」，丢包时 ReceivingNext 卡住不动，重传也跟着发不出去，
// 连接直接死锁——而单看这一侧的日志完全正常。
func TestAckCarriesReceivingWindow(t *testing.T) {
	w := &ackWriter{}
	l := NewAckList(w, 1350)
	l.Add(0, 100)
	l.Flush(200, 100, 7, 1, 512)

	if len(w.segs) != 1 {
		t.Fatalf("发出了 %d 个段，期望 1 个", len(w.segs))
	}
	ack := w.segs[0]
	if ack.ReceivingWindow != 512 {
		t.Errorf("ReceivingWindow = %d，期望 512", ack.ReceivingWindow)
	}
	if ack.ReceivingNext != 1 {
		t.Errorf("ReceivingNext = %d，期望 1", ack.ReceivingNext)
	}
}

// 余量随缓存占用递减，塞到底归零，绝不因为无符号减法绕成一个巨大的数——
// 那会让对端以为可以无限发。
func TestReceivingWindowRemainingShrinksToZero(t *testing.T) {
	const capacity = 4
	w := NewReceivingWindow(capacity)
	if got := w.Remaining(); got != capacity {
		t.Fatalf("空窗口余量 = %d，期望 %d", got, capacity)
	}
	// 从 1 开始塞，跳过 0：缺了队首，这些段都留在缓存里不会被交付出去。
	// 窗口范围是 [0, capacity)，所以能进去的是 1..capacity-1。
	for i := uint32(1); i < capacity; i++ {
		if !w.Accept(&DataSegment{Number: i}) {
			t.Fatalf("序号 %d 被拒，它应当在窗口内", i)
		}
		want := uint32(capacity) - i
		if got := w.Remaining(); got != want {
			t.Fatalf("塞入 %d 个后余量 = %d，期望 %d", i, got, want)
		}
	}
	// 补上队首，缓存被排空，余量应当回到满值
	w.Accept(&DataSegment{Number: 0})
	w.Drain()
	if got := w.Remaining(); got != capacity {
		t.Fatalf("排空后余量 = %d，期望回到 %d", got, capacity)
	}
}
