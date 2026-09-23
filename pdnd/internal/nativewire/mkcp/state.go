package mkcp

// 连接状态机。
//
// mKCP 跑在 UDP 上，没有 TCP 那样的四次挥手可以依赖，关闭必须自己走一遍
// 状态。六个状态分成三组：活跃、一方要关、双方都要销毁。
//
// 之所以要这么细：UDP 上「对方还在不在」是不确定的。如果只有开/关两态，
// 本地关闭时无法区分「已经通知了对端、在等它确认」和「对端也关了、可以
// 直接销毁」，结果要么过早释放（对端的重传打到一个不存在的会话上），
// 要么永远不释放（对端已经消失，我们还在等它回话）。

type State int32

const (
	// StateActive 双向都能收发。
	StateActive State = iota
	// StateReadyToClose 本地调用了 Close：不再收，但要把还没发完的数据
	// 送出去。发完才进 Terminating。
	StateReadyToClose
	// StatePeerClosed 对端关了写：我们还能继续发，但读到头了。
	StatePeerClosed
	// StateTerminating 本地要销毁，正在反复告知对端。
	StateTerminating
	// StatePeerTerminating 对端要销毁，我们给它一点时间收尾。
	StatePeerTerminating
	// StateTerminated 终态，资源可以释放。
	StateTerminated
)

func (s State) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateReadyToClose:
		return "ready-to-close"
	case StatePeerClosed:
		return "peer-closed"
	case StateTerminating:
		return "terminating"
	case StatePeerTerminating:
		return "peer-terminating"
	case StateTerminated:
		return "terminated"
	}
	return "unknown"
}

// Is 判断当前状态是否是给定几个之一。
func (s State) Is(states ...State) bool {
	for _, candidate := range states {
		if s == candidate {
			return true
		}
	}
	return false
}

// closedForRead 表示读到头了，可以给上层返回 EOF。
//
// 注意 StatePeerClosed 不在里面。对端的 Close 只说明「我不会再产生新
// 数据」，不代表已经发出去的都到了——Close 是搭在命令段上传过来的，
// 命令段不占发送窗口序号，会超过那些还在重传的数据段先到。收到它就
// 立刻 EOF 会把尾部数据截掉，而且丢包越多截得越狠。
//
// 真正安全的信号是 Terminate：对端只有在剩余数据全部被确认（或者等到
// 超时放弃）之后才会发它。所以 EOF 由 PeerTerminating 触发。
func (s State) closedForRead() bool {
	return s.Is(StateReadyToClose, StatePeerTerminating,
		StateTerminating, StateTerminated)
}

// closedForWrite 表示不该再接受上层写入。
//
// 包含 ReadyToClose：本地已经调过 Close，窗口里剩下的会继续发完，但不该
// 再收新的。漏掉它的话 Close() 之后还能写，那些数据发出去对端也不会读。
func (s State) closedForWrite() bool {
	return s.Is(StateReadyToClose, StatePeerClosed,
		StateTerminating, StatePeerTerminating, StateTerminated)
}

// nextOnLocalClose 是本地 Close() 时的状态转换。
//
// 返回 (新状态, 是否真的发生了转换)。已经在关闭路上的重复 Close 不该
// 把状态往回带——那会让一条正在销毁的连接重新开始等待。
func nextOnLocalClose(s State) (State, bool) {
	switch s {
	case StateActive:
		return StateReadyToClose, true
	case StatePeerClosed:
		// 对端已经关了，我们也关，那就没什么可等的了。
		return StateTerminating, true
	case StatePeerTerminating:
		return StateTerminated, true
	}
	return s, false
}

// nextOnPeerTerminate 是收到对端 Terminate 命令时的状态转换。
func nextOnPeerTerminate(s State) (State, bool) {
	switch s {
	case StateActive, StatePeerClosed:
		return StatePeerTerminating, true
	case StateReadyToClose:
		return StateTerminating, true
	case StateTerminating:
		// 双方都在销毁，可以收工了。
		return StateTerminated, true
	}
	return s, false
}

// nextOnPeerClose 是收到对端 Close 选项（对方不再发数据）时的转换。
func nextOnPeerClose(s State) (State, bool) {
	switch s {
	case StateActive:
		return StatePeerClosed, true
	case StateReadyToClose:
		// 两边都不发了，进入销毁流程。
		return StateTerminating, true
	}
	return s, false
}
