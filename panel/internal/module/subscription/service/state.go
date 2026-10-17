package service

import "time"

// State 是订阅的状态口径；全仓库只在这里定义，别的模块调 StateAt 或读 Summary.State。
type State string

// 三种状态口径。
const (
	// StateLive 生效：status=active 且到期时间在 now 之后。
	StateLive State = "live"
	// StateRevivable 可救回：不生效、续费窗口没关（含 status 还停在 active 但已走过到期日、过期扫描没赶上的）。
	StateRevivable State = "revivable"
	// StateEnded 已停用：续费窗口已关（renewal_closed_at 非空）。
	StateEnded State = "ended"
)

// StateAt 按库里的事实算 now 时刻的状态口径。
func StateAt(status string, periodEnd time.Time, renewalClosedAt *time.Time, now time.Time) State {
	if renewalClosedAt != nil {
		return StateEnded
	}
	if status == StatusActive && periodEnd.After(now) {
		return StateLive
	}
	return StateRevivable
}

// AcceptsChange 表示还能续费、换套餐、延期、作为流量包转入目标（未停用）。
func (st State) AcceptsChange() bool { return st != StateEnded }

// StateAt 算这份订阅在 now 时刻的状态口径。
func (s Subscription) StateAt(now time.Time) State {
	return StateAt(s.Status, s.PeriodEnd, s.RenewalClosedAt, now)
}

// transitions 是 status 列的转换表：active→active 续期，expired→active 续费或延期救回，active→expired 到期扫描。
// 没有 expired→expired：过期扫描关窗只写 renewal_closed_at、状态不变，直接走 repo.UpdateSubscription(from=expired)，
// 不经 transition()，也不要为它在这里加一条 expired→expired。
var transitions = map[string][]string{
	StatusActive:  {StatusActive, StatusExpired},
	StatusExpired: {StatusActive},
}

// CanTransition 查转换表。
func CanTransition(from, to string) bool {
	for _, t := range transitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// currentIndex 挑「当前订阅」的下标：生效的优先，再到期晚的优先，再创建晚的优先；空切片返回 -1。
func currentIndex(subs []Summary) int {
	best := -1
	for i := range subs {
		if best < 0 || betterCurrent(subs[i], subs[best]) {
			best = i
		}
	}
	return best
}

// betterCurrent 判断 a 是否比 b 更适合当「当前订阅」。
func betterCurrent(a, b Summary) bool {
	al, bl := a.State == StateLive, b.State == StateLive
	if al != bl {
		return al
	}
	if !a.PeriodEnd.Equal(b.PeriodEnd) {
		return a.PeriodEnd.After(b.PeriodEnd)
	}
	return a.CreatedAt.After(b.CreatedAt)
}
