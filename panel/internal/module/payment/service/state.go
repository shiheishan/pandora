package service

// 状态机：每个状态机一张转换表加一个纯函数；repo 的状态更新带 WHERE status = 旧状态，影响 0 行就是并发冲突。

// intentTransitions 是支付意图的转换表：在途意图只会走到一个终态，终态没有出边。
var intentTransitions = map[IntentStatus][]IntentStatus{
	IntentRequiresAction: {IntentSucceeded, IntentCancelled, IntentExpired},
}

// CanTransition 报告支付意图能否从 from 转到 to。
func CanTransition(from, to IntentStatus) bool {
	for _, next := range intentTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// lateTransitions 是挂账案的转换表：只有转入余额一种处理。
var lateTransitions = map[LateStatus][]LateStatus{
	LateSuspense: {LateApplied},
}

// CanTransitionLate 报告挂账案能否从 from 转到 to。
func CanTransitionLate(from, to LateStatus) bool {
	for _, next := range lateTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// 回调事件的处理状态：pending 只存在于事务内，提交时一定是 processed 或 ignored。
const (
	eventPending   = "pending"
	eventProcessed = "processed"
	eventIgnored   = "ignored"
)
