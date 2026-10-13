package node

import (
	"sort"

	"github.com/google/uuid"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
)

// 流量上报不丢。
//
// 内核的 GetTraffic 取出即清零，取出来的流量在面板确认落库之前只在 trafficBuffer 里：
//
//   - 一份报告含这台服务器各节点的条目，按 (节点, uid) 合并。
//   - inflight 是已经发出、还没收到 TrafficAck 的那一份，收到 ack 才丢。期间每个上报节拍、
//     每次新流的 Welcome 都原样重发（同一 report_id、同样内容）：面板可能已经落库、只是 ack
//     丢在断开的流上，原样重发才能让面板按 report_id 去重；把它和新流量合并成另一份，
//     面板就分不出来，只能重复计费。
//   - 一份至多 maxTrafficEntriesPerReport 条；多出来的留在 pending，上一份确认后再封下一份。
//   - pending 是还没封的流量，按 (节点, uid) 累加（上行、下行分别累加），所以面板不可达多久
//     内存都只随用户数增长、不随断线时长增长。inflight 确认之后，pending 立刻封成下一份发出。
//   - 上限 maxBufferedTrafficUsers 条（inflight + pending）。超限时先丢最旧的 inflight；
//     没有 inflight 时丢新出现的 key。丢弃都计数、记 Error。
//
// 缓冲只在内存里：进程崩溃最多丢一个节拍的流量（换栈之后再评估落盘）。

// maxBufferedTrafficUsers 是待报缓冲的条目上限（约每条 60 字节，13 万条约 8MB）。
const maxBufferedTrafficUsers = 1 << 17

// maxTrafficEntriesPerReport 是一份流量报告的条目上限（每条约 60 字节，5000 条远低于 gRPC 缺省 4MB 接收上限）。
const maxTrafficEntriesPerReport = 5000

// maxOnlineEntriesPerMessage 是一条 OnlineIPs 消息里的条目上限，超了分多条发。
const maxOnlineEntriesPerMessage = 5000

type trafficKey struct {
	node string
	uid  int64
}

type sealedReport struct {
	id      string
	entries []*nodev1.TrafficEntry
}

type trafficBuffer struct {
	inflight *sealedReport
	pending  map[trafficKey]*nodev1.TrafficEntry
	// droppedReports / droppedBytes 累计因超限丢掉的份数与字节。
	droppedReports int
	droppedBytes   int64
}

func (b *trafficBuffer) size() int {
	n := len(b.pending)
	if b.inflight != nil {
		n += len(b.inflight.entries)
	}
	return n
}

func (b *trafficBuffer) empty() bool { return b.size() == 0 }

// add 把内核取出的增量按 (节点, uid) 合并进 pending，返回因超限丢掉的字节数。
func (b *trafficBuffer) add(nodeID string, traffic []core.UserTraffic) (dropped int64) {
	if b.pending == nil {
		b.pending = make(map[trafficKey]*nodev1.TrafficEntry)
	}
	for _, t := range traffic {
		up, down := nonNegative(t.Upload), nonNegative(t.Download)
		if up == 0 && down == 0 {
			continue
		}
		k := trafficKey{node: nodeID, uid: t.ID}
		cur, exists := b.pending[k]
		if !exists && b.size() >= maxBufferedTrafficUsers {
			if b.inflight != nil {
				dropped += b.dropInflight()
			} else {
				b.droppedBytes += int64(up + down)
				dropped += int64(up + down)
				continue
			}
		}
		if !exists {
			cur = &nodev1.TrafficEntry{NodeId: nodeID, Uid: t.ID}
			b.pending[k] = cur
		}
		cur.Upload += up
		cur.Download += down
	}
	return dropped
}

func nonNegative(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

// dropInflight 丢掉 inflight 并计数，返回丢掉的字节数。
func (b *trafficBuffer) dropInflight() int64 {
	if b.inflight == nil {
		return 0
	}
	bytes := entriesBytes(b.inflight.entries)
	b.droppedReports++
	b.droppedBytes += bytes
	b.inflight = nil
	return bytes
}

// seal 在没有 inflight 时把 pending 封成新的一份，带新的 report_id。
// 条目按 (节点, uid) 排序，同一份重发时内容逐字节一致。一份至多 maxTrafficEntriesPerReport 条，
// 剩下的留在 pending，等这一份的 ack 之后再封下一份（一份太大会超过 gRPC 的接收上限被拒，
// 原样重发就会死循环）。
func (b *trafficBuffer) seal() {
	if b.inflight != nil || len(b.pending) == 0 {
		return
	}
	all := make([]*nodev1.TrafficEntry, 0, len(b.pending))
	for _, e := range b.pending {
		all = append(all, e)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].NodeId != all[j].NodeId {
			return all[i].NodeId < all[j].NodeId
		}
		return all[i].Uid < all[j].Uid
	})
	out := all[:min(len(all), maxTrafficEntriesPerReport)]
	for _, e := range out {
		delete(b.pending, trafficKey{node: e.NodeId, uid: e.Uid})
	}
	b.inflight = &sealedReport{id: uuid.NewString(), entries: out}
}

// bytes 是缓冲里 inflight 与 pending 的字节合计（日志用）。
func (b *trafficBuffer) bytes() int64 {
	var n int64
	if b.inflight != nil {
		n += entriesBytes(b.inflight.entries)
	}
	for _, e := range b.pending {
		n += int64(e.Upload + e.Download)
	}
	return n
}

func entriesBytes(entries []*nodev1.TrafficEntry) (n int64) {
	for _, e := range entries {
		n += int64(e.Upload + e.Download)
	}
	return n
}

// report 是上报节拍：取各入站的流量并交给面板，报在线地址，顺手把名单落盘。
func (s *Server) report() {
	for _, id := range s.order {
		if n := s.nodes[id]; n != nil && n.started {
			s.collectNodeTraffic(n)
		}
	}
	s.flushTraffic()
	s.reportOnline()
	s.flushUsersCache()
}

// collectNodeTraffic 从内核取出该入站的增量并入待报缓冲（删入站前也要调一次，最后一轮不能丢）。
func (s *Server) collectNodeTraffic(n *Node) {
	traffic, err := s.kernel.GetTraffic(n.tag)
	if err != nil {
		n.log.Error("读取流量失败", "err", err)
		return
	}
	s.bufferTraffic(n.id, traffic)
}

func (s *Server) bufferTraffic(nodeID string, traffic []core.UserTraffic) {
	if dropped := s.traffic.add(nodeID, traffic); dropped > 0 {
		s.log.Error("待报流量超过缓冲上限，已丢弃最旧的一批", "字节", dropped,
			"累计丢弃份数", s.traffic.droppedReports, "累计丢弃字节", s.traffic.droppedBytes)
	}
}

// flushTraffic 把待报缓冲交给面板：有 inflight 就原样重发，没有就封一份新的发出。
// 没连着或发送队列满就留在缓冲里，等下一个节拍或下一条流的 Welcome。
func (s *Server) flushTraffic() {
	if !s.connected || s.link == nil {
		return
	}
	s.traffic.seal()
	sealed := s.traffic.inflight
	if sealed == nil {
		return
	}
	if !s.link.SendTraffic(&nodev1.TrafficReport{ReportId: sealed.id, Entries: sealed.entries}) {
		s.log.Debug("流量上报没发出去，留待重发", "report_id", sealed.id, "条目", len(sealed.entries))
	}
}

// onTrafficAck：面板已落库这一份。丢掉 inflight，pending 有东西就立刻封下一份发出。
func (s *Server) onTrafficAck(reportID string) {
	if s.traffic.inflight == nil || s.traffic.inflight.id != reportID {
		s.log.Debug("收到对不上的流量回执，忽略", "report_id", reportID)
		return
	}
	s.traffic.inflight = nil
	if len(s.traffic.pending) > 0 {
		s.flushTraffic()
	}
}

// reportOnline 报各节点各用户的在线来源地址。设备键口径（IPv4 按地址、IPv6 按 /64）由内核保证，
// 这里原样转发；不留缓冲，没连着就跳过（下个节拍是新的快照）。
func (s *Server) reportOnline() {
	if !s.connected || s.link == nil {
		return
	}
	var entries []*nodev1.OnlineEntry
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil || !n.started {
			continue
		}
		online := s.kernel.OnlineIPs(n.tag)
		uids := make([]int64, 0, len(online))
		for uid, ips := range online {
			if len(ips) > 0 {
				uids = append(uids, uid)
			}
		}
		sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
		for _, uid := range uids {
			entries = append(entries, &nodev1.OnlineEntry{NodeId: n.id, Uid: uid, Ips: online[uid]})
		}
	}
	for len(entries) > 0 {
		k := min(len(entries), maxOnlineEntriesPerMessage)
		if !s.link.SendOnline(&nodev1.OnlineIPs{Entries: entries[:k]}) {
			s.log.Debug("在线地址没发出去，等下个节拍")
			return
		}
		entries = entries[k:]
	}
}
