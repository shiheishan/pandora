package node

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 流量上报不丢。
//
// 内核的 GetTraffic 取出即清零，原先上报一失败这一批就真没了（面板滚动重启
// 3 秒约有 5% 节点丢 1 分钟流量）。现在没被面板收下的流量留在 trafficBuffer：
//
//   - inflight 是已经发出过、结果不确定（传输错误、5xx、408、429）的那一份，
//     下一轮原样重发、带同一个 report_id。面板可能其实已经入账、只是回包丢了，
//     原样重发才能让面板按 (节点, report_id) 去重；把它和新流量合并成另一份
//     报文，面板就分不出来，只能重复计费。
//   - pending 是还没发出的流量，按 uid 合并（上行、下行分别累加），所以面板
//     不可达多久内存都只随用户数增长。inflight 收下之后，pending 在同一轮里
//     封成新的一份接着发。
//   - 面板明确拒收（4xx，408 / 429 除外）的那一份丢弃并计数：同样的内容重发
//     只会再被拒，留着会挡住后面所有上报。
//   - 上限 maxBufferedTrafficUsers 个 uid（inflight + pending）。超限时先丢最旧的
//     inflight；没有 inflight 时丢新出现的 uid。丢弃都计数、记 Error。

// maxBufferedTrafficUsers 是待报缓冲的 uid 条目上限（约每条 40 字节，13 万条约 5MB）。
const maxBufferedTrafficUsers = 1 << 17

type sealedReport struct {
	id      string
	traffic []core.UserTraffic
}

type trafficBuffer struct {
	inflight *sealedReport
	pending  map[int64]core.UserTraffic
	// droppedReports / droppedBytes 累计因拒收或超限丢掉的份数与字节。
	droppedReports int
	droppedBytes   int64
}

func (b *trafficBuffer) size() int {
	n := len(b.pending)
	if b.inflight != nil {
		n += len(b.inflight.traffic)
	}
	return n
}

func (b *trafficBuffer) empty() bool { return b.size() == 0 }

// pendingAndInflight 返回缓冲里全部还没被面板收下的流量（只读，日志用）。
func (b *trafficBuffer) pendingAndInflight() []core.UserTraffic {
	var out []core.UserTraffic
	if b.inflight != nil {
		out = append(out, b.inflight.traffic...)
	}
	for _, t := range b.pending {
		out = append(out, t)
	}
	return out
}

// add 把内核取出的增量按 uid 合并进 pending，返回因超限丢掉的字节数。
func (b *trafficBuffer) add(traffic []core.UserTraffic) (dropped int64) {
	if b.pending == nil {
		b.pending = make(map[int64]core.UserTraffic)
	}
	for _, t := range traffic {
		if t.Upload == 0 && t.Download == 0 {
			continue
		}
		cur, exists := b.pending[t.ID]
		if !exists && b.size() >= maxBufferedTrafficUsers {
			if b.inflight != nil {
				dropped += b.dropInflight()
			} else {
				b.droppedBytes += t.Upload + t.Download
				dropped += t.Upload + t.Download
				continue
			}
		}
		cur.ID = t.ID
		cur.Upload += t.Upload
		cur.Download += t.Download
		b.pending[t.ID] = cur
	}
	return dropped
}

// dropInflight 丢掉 inflight 并计数，返回丢掉的字节数。
func (b *trafficBuffer) dropInflight() int64 {
	if b.inflight == nil {
		return 0
	}
	bytes := trafficBytes(b.inflight.traffic)
	b.droppedReports++
	b.droppedBytes += bytes
	b.inflight = nil
	return bytes
}

// seal 在没有 inflight 时把 pending 封成新的一份，带新的 report_id。
func (b *trafficBuffer) seal() {
	if b.inflight != nil || len(b.pending) == 0 {
		return
	}
	out := make([]core.UserTraffic, 0, len(b.pending))
	for _, t := range b.pending {
		out = append(out, t)
	}
	b.inflight = &sealedReport{id: uuid.NewString(), traffic: out}
	b.pending = make(map[int64]core.UserTraffic)
}

func trafficBytes(traffic []core.UserTraffic) (n int64) {
	for _, t := range traffic {
		n += t.Upload + t.Download
	}
	return n
}

// report 上报流量与在线 IP。
func (n *Node) report(ctx context.Context) {
	if n.started {
		n.collectTraffic()
	}
	n.flushTraffic(ctx)

	if !n.started {
		return
	}
	if online := n.kernel.OnlineIPs(n.tag); len(online) > 0 {
		if err := n.client.Alive(ctx, online); err != nil {
			n.log.Warn("上报在线 IP 失败", "err", err)
		}
	}
}

// collectTraffic 从内核取出增量并入待报缓冲。
func (n *Node) collectTraffic() {
	traffic, err := n.kernel.GetTraffic(n.tag)
	if err != nil {
		n.log.Error("读取流量失败", "err", err)
		return
	}
	n.bufferTraffic(traffic)
}

func (n *Node) bufferTraffic(traffic []core.UserTraffic) {
	if dropped := n.traffic.add(traffic); dropped > 0 {
		n.log.Error("待报流量超过缓冲上限，已丢弃最旧的一批", "字节", dropped,
			"累计丢弃份数", n.traffic.droppedReports, "累计丢弃字节", n.traffic.droppedBytes)
	}
}

// flushTraffic 把待报缓冲交给面板：先重发 inflight，收下后再封、发 pending。
// 一轮最多两次请求；返回时没送出去的留给下一轮。
func (n *Node) flushTraffic(ctx context.Context) {
	for i := 0; i < 2; i++ {
		n.traffic.seal()
		sealed := n.traffic.inflight
		if sealed == nil {
			return
		}
		err := n.client.Push(ctx, sealed.traffic, sealed.id)
		if err == nil {
			n.traffic.inflight = nil
			continue
		}
		if panel.IsRejection(err) {
			bytes := n.traffic.dropInflight()
			n.log.Error("面板拒收流量上报，本份已丢弃", "err", err, "report_id", sealed.id, "字节", bytes,
				"累计丢弃份数", n.traffic.droppedReports)
			continue
		}
		n.log.Warn("上报流量失败，下一轮原样重发", "err", err, "report_id", sealed.id,
			"字节", trafficBytes(sealed.traffic), "待报用户数", n.traffic.size())
		return
	}
}
