package mkcp

import "testing"

// recordingWriter 记下被写出去的段，用来断言重传时机。
type recordingWriter struct {
	sent []uint32 // 依次写出的段序号，重发会出现多次
	err  error    // 非 nil 时模拟写失败
}

func (w *recordingWriter) Write(seg Segment) error {
	if w.err != nil {
		return w.err
	}
	w.sent = append(w.sent, seg.(*DataSegment).Number)
	return nil
}

func newWindow(t *testing.T, numbers ...uint32) (*SendingWindow, *recordingWriter) {
	t.Helper()
	w := &recordingWriter{}
	sw := NewSendingWindow(w, nil)
	for _, n := range numbers {
		sw.Push(&DataSegment{Number: n, Data: []byte("x")})
	}
	return sw, w
}

func TestClearRemovesUpToUna(t *testing.T) {
	sw, _ := newWindow(t, 1, 2, 3, 4, 5)
	// una=3 意思是「3 之前的都收齐了」，所以 1、2 该走，3 要留着。
	sw.Clear(3)
	if sw.Len() != 3 {
		t.Fatalf("剩余 %d 个，期望 3 个", sw.Len())
	}
	if got := sw.FirstNumber(); got != 3 {
		t.Errorf("首个序号 = %d，期望 3", got)
	}
}

func TestRemoveTakesOutSingleNumber(t *testing.T) {
	sw, _ := newWindow(t, 1, 2, 3)
	if !sw.Remove(2) {
		t.Fatal("删 2 应当成功")
	}
	if sw.Len() != 2 {
		t.Fatalf("剩余 %d 个，期望 2 个", sw.Len())
	}
	// 删不存在的序号要如实返回 false，而不是静默当成删过了——
	// 上层据此判断这个 ACK 是不是重复的。
	if sw.Remove(2) {
		t.Error("重复删同一个序号应当返回 false")
	}
	if sw.Remove(99) {
		t.Error("删窗口外的序号应当返回 false")
	}
}

// 首次 Flush 应该把所有段都发出去（都还没发过）。
func TestFlushSendsUnsentSegments(t *testing.T) {
	sw, w := newWindow(t, 1, 2, 3)
	sent := sw.Flush(1000, 200, 10)
	if sent != 3 || len(w.sent) != 3 {
		t.Fatalf("发出 %d 个（记录 %d 个），期望 3 个", sent, len(w.sent))
	}
}

// 已发过且未到超时的段，不该被重复发送。
func TestFlushSkipsSegmentsBeforeTimeout(t *testing.T) {
	sw, w := newWindow(t, 1, 2)
	sw.Flush(1000, 200, 10) // 首发，超时设为 1200
	w.sent = nil

	if n := sw.Flush(1100, 200, 10); n != 0 {
		t.Fatalf("未到超时却发了 %d 个", n)
	}
	// 到点了才重发
	if n := sw.Flush(1201, 200, 10); n != 2 {
		t.Fatalf("超时后重发 %d 个，期望 2 个", n)
	}
}

// maxInFlight 是拥塞控制给的额度，必须守住。
func TestFlushRespectsMaxInFlight(t *testing.T) {
	sw, _ := newWindow(t, 1, 2, 3, 4, 5)
	if n := sw.Flush(1000, 200, 2); n != 2 {
		t.Fatalf("额度为 2 却发了 %d 个", n)
	}
}

// 写失败要中断本轮：底下多半是 UDP 缓冲满了，接着写只是徒劳。
func TestFlushStopsOnWriteError(t *testing.T) {
	sw, w := newWindow(t, 1, 2, 3)
	w.err = errWriteFailed
	if n := sw.Flush(1000, 200, 10); n != 0 {
		t.Fatalf("写失败却报告发出 %d 个", n)
	}
}

var errWriteFailed = &writeError{}

type writeError struct{}

func (*writeError) Error() string { return "写失败" }

// 快速重传：对端确认了 3，说明 1、2 大概率丢了，它们的超时该提前。
func TestHandleFastAckAdvancesEarlierSegments(t *testing.T) {
	sw, w := newWindow(t, 1, 2, 3)
	sw.Flush(1000, 300, 10) // 首发，三个的超时都是 1300
	w.sent = nil

	sw.HandleFastAck(3, 300) // 1、2 的超时各减 100，变成 1200

	// 1200 时 1、2 该重发，3 还没到点
	if n := sw.Flush(1201, 300, 10); n != 2 {
		t.Fatalf("快速重传发了 %d 个，期望 2 个", n)
	}
	for _, num := range w.sent {
		if num == 3 {
			t.Error("序号 3 不该被快速重传——它已经被确认了")
		}
	}
}

// 没发过的段不参与快速重传：它们没有「重传」可言，提前超时没有意义。
func TestHandleFastAckIgnoresUnsentSegments(t *testing.T) {
	sw, _ := newWindow(t, 1, 2)
	sw.HandleFastAck(2, 300)
	// timeout 仍是零值，Flush 时按「没发过」处理，正常首发
	if n := sw.Flush(100, 300, 10); n != 2 {
		t.Fatalf("首发 %d 个，期望 2 个", n)
	}
}

// 丢包率是拥塞控制的输入。首发不算丢包，重发才算。
func TestPacketLossRateCountsRetransmitsOnly(t *testing.T) {
	var reported []uint32
	w := &recordingWriter{}
	sw := NewSendingWindow(w, func(rate uint32) { reported = append(reported, rate) })
	for _, n := range []uint32{1, 2} {
		sw.Push(&DataSegment{Number: n, Data: []byte("x")})
	}

	sw.Flush(1000, 200, 10)
	if len(reported) != 1 || reported[0] != 0 {
		t.Fatalf("首发的丢包率 = %v，期望 [0]", reported)
	}

	// 两个都超时重发 → 2 个重发 / 2 个在途 = 100%
	reported = nil
	sw.Flush(1300, 200, 10)
	if len(reported) != 1 || reported[0] != 100 {
		t.Fatalf("全部重发时丢包率 = %v，期望 [100]", reported)
	}
}

// 时间戳是 uint32，约 49.7 天回绕一次。回绕点附近判反的后果是连接卡死
// （超时被当成没到）或疯狂重发（没到被当成超时），两种都很难现场诊断。
func TestTimeComparisonSurvivesWraparound(t *testing.T) {
	// 用变量而不是常量：有类型常量的加法在编译期就报溢出，
	// 而我们要测的正是运行时的回绕。
	nearMax := ^uint32(0) - 100

	if !timeAfter(nearMax+200, nearMax) {
		t.Error("跨过回绕点后，晚的时刻应当被判为更晚")
	}
	if timeAfter(nearMax, nearMax+200) {
		t.Error("跨过回绕点后，早的时刻不该被判为更晚")
	}
	if timeAfter(5, 5) {
		t.Error("相等不算更晚")
	}

	// 放到真实的重传路径上验证一次
	sw, w := newWindow(t, 1)
	sw.Flush(nearMax, 200, 10) // 超时 = nearMax+200，已经绕过 0
	w.sent = nil
	if n := sw.Flush(nearMax+100, 200, 10); n != 0 {
		t.Error("回绕后未到超时却重发了")
	}
	if n := sw.Flush(nearMax+201, 200, 10); n != 1 {
		t.Error("回绕后到了超时却没重发")
	}
}

// 序号也会回绕，累积确认必须跟着回绕走。
func TestNumberComparisonSurvivesWraparound(t *testing.T) {
	nearMax := ^uint32(0) - 2
	if !numberAfter(1, nearMax) {
		t.Error("回绕后的小序号应当被判为更新")
	}
	if numberAfter(nearMax, 1) {
		t.Error("回绕前的大序号不该被判为更新")
	}

	sw, _ := newWindow(t, nearMax, nearMax+1, nearMax+2 /* 溢出成 0 */, 1)
	sw.Clear(1) // 确认到 1 之前，即前三个
	if sw.Len() != 1 {
		t.Fatalf("回绕处累积确认后剩 %d 个，期望 1 个", sw.Len())
	}
	if got := sw.FirstNumber(); got != 1 {
		t.Errorf("剩下的序号 = %d，期望 1", got)
	}
}

// Clear 之后底层数组不该继续持有已确认段的引用，否则长连接下这些
// 段一直不被回收。
func TestClearReleasesReferences(t *testing.T) {
	sw, _ := newWindow(t, 1, 2, 3)
	sw.Clear(3)
	// 复用底层数组时，被挪走的位置上不该还留着旧指针
	full := sw.entries[:cap(sw.entries)]
	for i := sw.Len(); i < len(full); i++ {
		if full[i].seg != nil && full[i].seg.Number < 3 {
			t.Fatalf("下标 %d 仍持有已确认的段 %d", i, full[i].seg.Number)
		}
	}
}
