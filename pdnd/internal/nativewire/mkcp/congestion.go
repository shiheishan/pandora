package mkcp

import "sync"

// RTT 估算与拥塞窗口。
//
// 这一层决定「隔多久算超时」和「一次能在途多少个段」，是吞吐和抗丢包的
// 主要旋钮。它不碰字节，所以和 xray 的差异不影响互通，只影响快慢。

// RoundTripInfo 按 RFC 6298 估算往返时间与重传超时。
//
// 并发安全：发送和接收在不同 goroutine 上更新它。
type RoundTripInfo struct {
	mu sync.RWMutex
	// srtt 是平滑后的 RTT，variation 是它的波动幅度。
	srtt      uint32
	variation uint32
	rto       uint32
	// minRtt 是见过的最小 RTT，作为 srtt 的下限。链路本身的物理延迟不会
	// 低于它，srtt 被几个异常样本拉到不合理的低位会导致过早重传。
	minRtt uint32
	// updatedAt 是上次更新时刻，用来给对端 RTO 上锁（见 UpdatePeerRTO）。
	updatedAt timestamp
}

// 重传超时的边界。
const (
	// rtoMax 挡住病态链路：再差也不该等超过 10 秒才重传，那时用户早就
	// 认为连接死了。
	rtoMax = 10000
	// rtoMin 挡住过于激进的重传。局域网内 RTT 可能不到 1ms，按它算出的
	// RTO 会让轻微抖动就触发重传，白白浪费带宽。
	rtoMin = 100
)

// Update 用一个新的 RTT 样本更新估算。
func (info *RoundTripInfo) Update(rtt uint32, current timestamp) {
	// 异常大的样本多半是时钟问题或者对端回显了错误的时间戳，采纳它会把
	// RTO 拉到天上，之后很久都不会重传。
	if rtt > 0x7FFFFFFF {
		return
	}
	info.mu.Lock()
	defer info.mu.Unlock()

	if info.minRtt == 0 || rtt < info.minRtt {
		info.minRtt = rtt
	}

	if info.srtt == 0 {
		info.srtt = rtt
		info.variation = rtt / 2
	} else {
		// 先算绝对差再更新，不能直接 rtt - srtt：这是 uint32，
		// 样本比均值小的时候会下溢成一个巨大的数。
		delta := rtt - info.srtt
		if info.srtt > rtt {
			delta = info.srtt - rtt
		}
		info.variation = (3*info.variation + delta) / 4
		info.srtt = (7*info.srtt + rtt) / 8
		if info.srtt < info.minRtt {
			info.srtt = info.minRtt
		}
	}

	rto := info.srtt + 4*info.variation
	if info.minRtt >= 4*info.variation {
		// 波动远小于基础延迟时，4 倍波动是过度保守的余量。
		rto = info.srtt + info.variation
	}
	// 多留 25%：估算再准也架不住突发排队。
	rto = rto * 5 / 4
	info.rto = clampRTO(rto)
	info.updatedAt = current
}

// UpdatePeerRTO 采纳对端在 Ping 里告知的 RTO。
//
// 三秒内只认一次：对端每个 Ping 都带 RTO，来一个改一次会让我们的重传节奏
// 完全被对方牵着走，而它看到的链路状况和我们不一定相同。
func (info *RoundTripInfo) UpdatePeerRTO(rto uint32, current timestamp) {
	info.mu.Lock()
	defer info.mu.Unlock()
	if info.updatedAt != 0 && current-info.updatedAt < 3000 {
		return
	}
	info.updatedAt = current
	info.rto = clampRTO(rto)
}

// Timeout 是当前的重传超时。没有任何样本时给一个保守的初值——
// 返回 0 会让发送窗口把所有段判成立即超时。
func (info *RoundTripInfo) Timeout() uint32 {
	info.mu.RLock()
	defer info.mu.RUnlock()
	if info.rto == 0 {
		return rtoMin
	}
	return info.rto
}

// SmoothedTime 是平滑后的 RTT。
func (info *RoundTripInfo) SmoothedTime() uint32 {
	info.mu.RLock()
	defer info.mu.RUnlock()
	return info.srtt
}

func clampRTO(rto uint32) uint32 {
	if rto < rtoMin {
		return rtoMin
	}
	if rto > rtoMax {
		return rtoMax
	}
	return rto
}

//------------------------------------------------------------------------------
// 拥塞窗口
//------------------------------------------------------------------------------

// CongestionWindow 按丢包率增减在途段数的额度。
//
// # 与 xray 的一处差异
//
// xray 在把额度交给发送窗口之前会 `cwnd *= 20`（它源码里就标着 magic），
// 效果是拥塞窗口基本不起作用——算出来多少都会被放大二十倍，几乎总是大于
// 实际待发段数。这里不跟那个放大：额度就是段数，该多少是多少。
//
// 后果是我们在丢包时比 xray 更保守，吞吐可能低一些。这不影响互通——
// 拥塞控制是发送方的自主决定，对端只看收到什么，不关心我们怎么决定发多少。
// 真跑起来发现吞吐不够再调，比一上来就抄一个自己都不理解的放大系数强。
type CongestionWindow struct {
	// window 是当前额度（段数）。
	window uint32
	// base 是配置给的基准，也是上限的一半。
	base uint32
	// enabled 为 false 时不做任何调整，始终返回 base——有些链路（比如
	// 专线）丢包率天然低，动态调整只会带来抖动。
	enabled bool
}

// 拥塞窗口的调整参数。
const (
	// lossHigh 以上算拥塞，收缩窗口。15% 是 mKCP 的经验值：UDP 链路上
	// 个位数丢包很常见，阈值太低会让窗口一直缩着上不去。
	lossHigh = 15
	// lossLow 以下算通畅，可以放开。留出 5%~15% 的中间地带不动作，
	// 避免在阈值附近来回震荡。
	lossLow = 5
	// windowFloor 是额度下限。再拥塞也要能发几个段出去，否则连探测链路
	// 是否恢复的机会都没有。
	windowFloor = 16
)

func NewCongestionWindow(base uint32, enabled bool) *CongestionWindow {
	if base < windowFloor {
		base = windowFloor
	}
	return &CongestionWindow{window: base, base: base, enabled: enabled}
}

// Size 是当前允许在途的段数。
func (c *CongestionWindow) Size() uint32 {
	if !c.enabled {
		return c.base
	}
	return c.window
}

// OnPacketLoss 根据一轮发送的重传占比调整额度。
//
// 乘性减、加性增：拥塞时快速退让，通畅时缓慢试探。反过来会在拥塞时
// 加剧拥塞。
func (c *CongestionWindow) OnPacketLoss(lossRate uint32) {
	if !c.enabled {
		return
	}
	switch {
	case lossRate >= lossHigh:
		c.window = 3 * c.window / 4
	case lossRate <= lossLow:
		c.window += c.window / 4
	default:
		return // 中间地带不动，避免震荡
	}
	if c.window < windowFloor {
		c.window = windowFloor
	}
	if c.window > 2*c.base {
		c.window = 2 * c.base
	}
}
