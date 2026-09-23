package mkcp

import "testing"

// 没有样本时不能返回 0——发送窗口会把 0 当成「立刻超时」，
// 一条新连接上来就会把所有段疯狂重发。
func TestTimeoutHasSaneDefault(t *testing.T) {
	var info RoundTripInfo
	if got := info.Timeout(); got != rtoMin {
		t.Fatalf("无样本时 RTO = %d，期望 %d", got, rtoMin)
	}
}

func TestUpdateSeedsFromFirstSample(t *testing.T) {
	var info RoundTripInfo
	info.Update(200, 1000)
	if got := info.SmoothedTime(); got != 200 {
		t.Errorf("首个样本后 srtt = %d，期望 200", got)
	}
	// srtt=200, variation=100, minRtt=200 → 200 >= 400 不成立
	// → rto = 200 + 400 = 600，再 ×5/4 = 750
	if got := info.Timeout(); got != 750 {
		t.Errorf("首个样本后 RTO = %d，期望 750", got)
	}
}

// 样本比均值小的时候，绝对差不能靠 uint32 减法直接得出——
// 会下溢成天文数字，把 variation 和 RTO 一起拉爆。
func TestUpdateHandlesSampleBelowAverage(t *testing.T) {
	var info RoundTripInfo
	info.Update(500, 1000)
	before := info.Timeout()
	info.Update(100, 1100) // 远小于当前 srtt
	after := info.Timeout()

	if after > rtoMax {
		t.Fatalf("小样本把 RTO 拉到了 %d（上限 %d），说明发生了下溢", after, rtoMax)
	}
	if after >= before*2 {
		t.Errorf("RTT 变小反而让 RTO 从 %d 涨到 %d", before, after)
	}
}

// 异常大的样本要拒绝：多半是时钟问题或对端回显了错误时间戳，
// 采纳它会让之后很久都不重传。
func TestUpdateRejectsAbsurdSample(t *testing.T) {
	var info RoundTripInfo
	info.Update(200, 1000)
	before := info.Timeout()
	info.Update(0x7FFFFFFF+1, 1100)
	if info.Timeout() != before {
		t.Error("异常样本被采纳了")
	}
}

func TestRTOStaysWithinBounds(t *testing.T) {
	var info RoundTripInfo
	// 极小 RTT（局域网）：RTO 不能低到让抖动就触发重传
	info.Update(1, 1000)
	if got := info.Timeout(); got < rtoMin {
		t.Errorf("极小 RTT 下 RTO = %d，低于下限 %d", got, rtoMin)
	}
	// 极大 RTT：RTO 不能高到用户以为连接死了
	var slow RoundTripInfo
	for i := 0; i < 20; i++ {
		slow.Update(30000, timestamp(1000+i*100))
	}
	if got := slow.Timeout(); got > rtoMax {
		t.Errorf("极大 RTT 下 RTO = %d，超过上限 %d", got, rtoMax)
	}
}

// srtt 不该被几个异常低的样本拉到物理延迟以下。
func TestSmoothedTimeRespectsMinRTT(t *testing.T) {
	var info RoundTripInfo
	for i := 0; i < 10; i++ {
		info.Update(200, timestamp(1000+i*100))
	}
	if got := info.SmoothedTime(); got < 200 {
		t.Errorf("srtt = %d，不该低于见过的最小 RTT 200", got)
	}
}

// 对端的 RTO 三秒内只认一次，否则我们的重传节奏被对方牵着走。
func TestUpdatePeerRTOIsRateLimited(t *testing.T) {
	var info RoundTripInfo
	info.Update(200, 1000) // updatedAt = 1000
	info.UpdatePeerRTO(5000, 1500)
	if info.Timeout() == 5000 {
		t.Error("三秒内的对端 RTO 不该被采纳")
	}
	info.UpdatePeerRTO(5000, 4500) // 距上次超过 3 秒
	if info.Timeout() != 5000 {
		t.Errorf("超过三秒后应当采纳，实际 RTO = %d", info.Timeout())
	}
}

// 对端给的 RTO 同样要夹在边界内——它可能配错，也可能是恶意的。
func TestUpdatePeerRTOIsClamped(t *testing.T) {
	var info RoundTripInfo
	info.UpdatePeerRTO(999999, 10000)
	if got := info.Timeout(); got > rtoMax {
		t.Errorf("对端的超大 RTO 未被夹住：%d", got)
	}
}

//------------------------------------------------------------------------------
// 拥塞窗口
//------------------------------------------------------------------------------

func TestCongestionShrinksOnHighLoss(t *testing.T) {
	c := NewCongestionWindow(64, true)
	c.OnPacketLoss(20)
	if got := c.Size(); got != 48 { // 64 * 3/4
		t.Fatalf("高丢包后窗口 = %d，期望 48", got)
	}
}

func TestCongestionGrowsOnLowLoss(t *testing.T) {
	c := NewCongestionWindow(64, true)
	c.OnPacketLoss(0)
	if got := c.Size(); got != 80 { // 64 + 64/4
		t.Fatalf("低丢包后窗口 = %d，期望 80", got)
	}
}

// 中间地带不动作，避免在阈值附近来回震荡。
func TestCongestionHoldsInMiddleBand(t *testing.T) {
	c := NewCongestionWindow(64, true)
	for _, rate := range []uint32{6, 10, 14} {
		c.OnPacketLoss(rate)
		if got := c.Size(); got != 64 {
			t.Fatalf("丢包率 %d%% 时窗口变成了 %d，应当保持 64", rate, got)
		}
	}
}

// 再拥塞也要留出探测余地，否则永远发现不了链路已经恢复。
func TestCongestionNeverDropsBelowFloor(t *testing.T) {
	c := NewCongestionWindow(64, true)
	for i := 0; i < 50; i++ {
		c.OnPacketLoss(100)
	}
	if got := c.Size(); got < windowFloor {
		t.Fatalf("持续丢包后窗口 = %d，低于下限 %d", got, windowFloor)
	}
}

func TestCongestionCapsAtTwiceBase(t *testing.T) {
	c := NewCongestionWindow(64, true)
	for i := 0; i < 50; i++ {
		c.OnPacketLoss(0)
	}
	if got := c.Size(); got > 128 {
		t.Fatalf("持续通畅后窗口 = %d，超过基准的两倍", got)
	}
}

// 关掉拥塞控制时窗口恒为基准值，不受丢包影响。
func TestCongestionDisabledStaysAtBase(t *testing.T) {
	c := NewCongestionWindow(64, false)
	c.OnPacketLoss(100)
	if got := c.Size(); got != 64 {
		t.Fatalf("关闭时窗口 = %d，期望恒为 64", got)
	}
}

// 基准值低于下限时要抬上来，否则一开始就没有可用额度。
func TestCongestionBaseRespectsFloor(t *testing.T) {
	c := NewCongestionWindow(4, true)
	if got := c.Size(); got < windowFloor {
		t.Fatalf("基准被配得过低时窗口 = %d，应当抬到 %d", got, windowFloor)
	}
}
