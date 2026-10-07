package node

import (
	"context"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// StartupOrder 让同一进程里的节点冷启动时按 config.json 的 nodes[] 顺序首装入站。
//
// 同机两个节点配到同一个端口时，内核按「先登记者赢」裁决（kernel 端口登记表）。
// 各节点的 Run 是并发起的，谁先装上原先取决于谁先从面板拿到配置——goroutine
// 竞速，每次重启赢家可能换人（审计 E1：6 次重启赢家 3:3）。这里规定：第 i 个
// 节点要等前 i 个节点都走完冷启动（装上了、或确定装不上），才轮到自己装。拉配置
// 照样并发，只有「装」排队，所以冷启动总耗时约等于最慢的那次拉取，而不是相加。
// 冷启动之后的运行期变更不排队：已占着端口的一直占着，后来者按退避重试。
type StartupOrder struct {
	done []chan struct{}
	once []sync.Once
}

// NewStartupOrder 为 n 个节点建一个启动次序。
func NewStartupOrder(n int) *StartupOrder {
	o := &StartupOrder{done: make([]chan struct{}, n), once: make([]sync.Once, n)}
	for i := range o.done {
		o.done[i] = make(chan struct{})
	}
	return o
}

func (o *StartupOrder) wait(ctx context.Context, index int) {
	for i := 0; i < index && i < len(o.done); i++ {
		select {
		case <-o.done[i]:
		case <-ctx.Done():
			return
		}
	}
}

func (o *StartupOrder) finish(index int) {
	if index >= 0 && index < len(o.done) {
		o.once[index].Do(func() { close(o.done[index]) })
	}
}

// SetStartupOrder 把节点挂进启动次序，index 是它在 nodes[] 里的位置。
func (n *Node) SetStartupOrder(order *StartupOrder, index int) {
	n.order, n.orderIndex = order, index
}

// awaitStartupTurn 在冷启动期间首次装配置前排队；冷启动结束后（或没挂次序）立即返回。
func (n *Node) awaitStartupTurn() {
	if n.order == nil || n.lifeCtx == nil {
		return
	}
	n.order.wait(n.lifeCtx, n.orderIndex)
}

// finishStartup 宣告本节点冷启动结束，之后的节点可以装了；之后不再排队。
func (n *Node) finishStartup() {
	if n.order != nil {
		n.order.finish(n.orderIndex)
		n.order = nil
	}
}

// startup 是冷启动：先问面板；面板不可达、入站没起来时用落盘缓存起服务。
//
// 面板明确拒绝这个节点（4xx，408 / 429 除外——身份吊销、节点被删）时不用
// 缓存：那不是「面板不可达」，拿缓存绕过去等于让面板管不住节点。
func (n *Node) startup(ctx context.Context) {
	defer n.finishStartup()
	cfgErr, usersErr := n.syncOnceErr(ctx)
	if ctx.Err() != nil {
		return
	}
	if cfgErr != nil {
		n.log.Error("同步配置失败", "err", cfgErr)
	}
	if usersErr != nil {
		n.log.Error("同步用户失败", "err", usersErr)
	}
	if n.cache == nil {
		return
	}
	if !n.started {
		if panel.IsRejection(cfgErr) {
			n.log.Warn("面板明确拒绝了这个节点，不用落盘缓存起服务", "err", cfgErr)
			return
		}
		if err := n.loadConfigFromCache(); err != nil {
			n.log.Warn("没能用落盘缓存起服务", "err", err)
			return
		}
		n.fromCache = true
		n.log.Warn("面板不可达或配置未能装上，已用落盘缓存的配置起服务；面板恢复后自动切回",
			"err", cfgErr)
		if err := n.loadUsersFromCache(); err != nil {
			n.log.Warn("落盘用户名单不可用，等面板恢复后同步", "err", err)
		}
		return
	}
	// 配置拿到了、用户名单没拿到（面板半可用）：先用缓存的名单，别带空表跑。
	if usersErr != nil && !panel.IsRejection(usersErr) && len(n.known) == 0 && n.userVersion == "" {
		if err := n.loadUsersFromCache(); err == nil {
			n.log.Warn("用户名单暂时拉不到，已先用落盘缓存的名单", "err", usersErr)
		}
	}
}

// Shutdown 是进程退出时的最后一次上报，在 Run 返回之后调用。
//
// drained 是内核关停后交出的本入站最后一轮流量（haveDrained 为真时）：先关内核、
// 让在途 TCP 连接把流量入账，再报——原先顺序反了，每次重启、升级都丢掉在途
// 连接的流量。内核不支持关停交流量时（haveDrained 为假）退回先取再报。
func (n *Node) Shutdown(ctx context.Context, drained []core.UserTraffic, haveDrained bool) {
	if haveDrained {
		n.bufferTraffic(drained)
	} else if n.started {
		n.collectTraffic()
	}
	n.flushTraffic(ctx)
	if !n.traffic.empty() {
		n.log.Error("退出前流量没能交给面板，这部分已丢失", "字节",
			trafficBytes(n.traffic.pendingAndInflight()), "累计丢弃字节", n.traffic.droppedBytes)
	}
	n.flushUsersCache()
}
