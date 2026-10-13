package node

import (
	"context"
	"fmt"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// entriesOf 把一份流量报告摊成 "<节点>/<uid>:<上>/<下>"，便于比对。
func entriesOf(r *nodev1.TrafficReport) []string {
	out := make([]string, 0, len(r.GetEntries()))
	for _, e := range r.GetEntries() {
		out = append(out, fmt.Sprintf("%s/%d:%d/%d", e.GetNodeId(), e.GetUid(), e.GetUpload(), e.GetDownload()))
	}
	return out
}

// twoNodes 起一个连着的 Server，装好 n1、n2 两个节点。
func twoNodes(t *testing.T) (*testServer, *fakeKernel) {
	t.Helper()
	k := newFakeKernel()
	ts := newTestServer(t, k, "")
	ts.welcome()
	ts.config(portConfig(t, "n1", "vless", 20001), portConfig(t, "n2", "trojan", 20002))
	return ts, k
}

// 一份报告含各节点条目，按 (节点, uid) 合并、排序；负数与全零的条目不报。
func TestTrafficMergedPerNodeAndUser(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n2", trafficOf(1, 5, 6))
	k.feed("n1", trafficOf(2, 1, 1), trafficOf(1, 10, 20), trafficOf(2, 3, 4), trafficOf(3, -5, 0), trafficOf(4, 0, 0))
	ts.report()
	reports := ts.link.trafficReports()
	if len(reports) != 1 || reports[0].GetReportId() == "" {
		t.Fatalf("报告 = %v", reports)
	}
	assertStrings(t, "条目", entriesOf(reports[0]), "n1/1:10/20", "n1/2:4/5", "n2/1:5/6")
}

// 没收到 TrafficAck 前每个节拍原样重发（同 report_id、同内容），新流量累进 pending；
// 收到 ack 后 pending 立刻封成下一份发出。
func TestTrafficResentUntilAckedThenNextSealed(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n1", trafficOf(1, 10, 20))
	ts.report()
	k.feed("n1", trafficOf(1, 1, 1))
	ts.report()
	reports := ts.link.trafficReports()
	if len(reports) != 2 || reports[0].GetReportId() != reports[1].GetReportId() {
		t.Fatalf("没原样重发：%v", reports)
	}
	assertStrings(t, "重发内容", entriesOf(reports[1]), "n1/1:10/20")

	ts.handleEvent(panel.Event{Kind: panel.EventTrafficAck, ReportID: "别的"})
	if len(ts.link.trafficReports()) != 2 || ts.traffic.inflight == nil {
		t.Fatal("对不上的回执不该丢 inflight")
	}
	ts.handleEvent(panel.Event{Kind: panel.EventTrafficAck, ReportID: reports[0].GetReportId()})
	reports = ts.link.trafficReports()
	if len(reports) != 3 || reports[2].GetReportId() == reports[0].GetReportId() {
		t.Fatalf("ack 后没发下一份：%v", reports)
	}
	assertStrings(t, "下一份", entriesOf(reports[2]), "n1/1:1/1")
	ts.handleEvent(panel.Event{Kind: panel.EventTrafficAck, ReportID: reports[2].GetReportId()})
	if !ts.traffic.empty() {
		t.Fatal("全部确认后缓冲应清空")
	}
}

// 断线期间流量留在缓冲里按 (节点, uid) 累加；新流 Welcome 时先原样重发 inflight。
func TestTrafficBufferedWhileDisconnectedAndResentOnWelcome(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n1", trafficOf(1, 10, 20))
	ts.report()
	first := ts.link.trafficReports()[0]
	ts.disconnect()
	k.feed("n1", trafficOf(1, 1, 2))
	ts.report()
	k.feed("n1", trafficOf(1, 3, 4))
	ts.report()
	if len(ts.link.trafficReports()) != 1 {
		t.Fatal("断线时不该发流量")
	}
	ts.welcome()
	reports := ts.link.trafficReports()
	if len(reports) != 2 || reports[1].GetReportId() != first.GetReportId() {
		t.Fatalf("Welcome 时没原样重发 inflight：%v", reports)
	}
	ts.handleEvent(panel.Event{Kind: panel.EventTrafficAck, ReportID: first.GetReportId()})
	reports = ts.link.trafficReports()
	assertStrings(t, "断线期间累加", entriesOf(reports[len(reports)-1]), "n1/1:4/6")
}

// 发送队列满时同样留在缓冲里，下个节拍再发同一份。
func TestTrafficKeptWhenSendFails(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n1", trafficOf(1, 10, 20))
	ts.link.setDown(true)
	ts.report()
	if ts.traffic.inflight == nil {
		t.Fatal("没发出去也应封好留着")
	}
	id := ts.traffic.inflight.id
	ts.link.setDown(false)
	ts.report()
	if reports := ts.link.trafficReports(); len(reports) != 1 || reports[0].GetReportId() != id {
		t.Fatalf("恢复后应发同一份：%v", reports)
	}
}

// 超过缓冲上限：有 inflight 先丢 inflight；没有就丢新出现的 key，都计数。
func TestTrafficBufferCap(t *testing.T) {
	var b trafficBuffer
	big := make([]core.UserTraffic, maxBufferedTrafficUsers)
	for i := range big {
		big[i] = trafficOf(int64(i+1), 1, 0)
	}
	if dropped := b.add("n1", big); dropped != 0 {
		t.Fatalf("没超限也丢了 %d", dropped)
	}
	if dropped := b.add("n1", []core.UserTraffic{trafficOf(1, 1, 0), trafficOf(-1, 7, 0)}); dropped != 7 {
		t.Fatalf("没有 inflight 时应丢新 key 的 7 字节，丢了 %d", dropped)
	}
	if b.droppedBytes != 7 || b.droppedReports != 0 || b.size() != maxBufferedTrafficUsers {
		t.Fatalf("计数不对：%+v size=%d", b.droppedBytes, b.size())
	}
	b.seal()
	if dropped := b.add("n2", []core.UserTraffic{trafficOf(1, 2, 3)}); dropped != maxTrafficEntriesPerReport+1 {
		t.Fatalf("应丢掉整份 inflight（uid 1 两字节、其余各一字节），丢了 %d 字节", dropped)
	}
	if b.inflight != nil || b.droppedReports != 1 || len(b.pending) != maxBufferedTrafficUsers-maxTrafficEntriesPerReport+1 {
		t.Fatalf("丢 inflight 后状态不对：reports=%d pending=%d", b.droppedReports, len(b.pending))
	}
}

// pending 超过单份上限：分多份发，每份收到 ack 之后才发下一份，内容不重不漏。
func TestTrafficSplitIntoReportsAckedOneByOne(t *testing.T) {
	ts, k := twoNodes(t)
	total := 2*maxTrafficEntriesPerReport + 1
	batch := make([]core.UserTraffic, total)
	for i := range batch {
		batch[i] = trafficOf(int64(i+1), 1, 2)
	}
	k.feed("n1", batch...)
	ts.report()
	seen := make(map[int64]bool, total)
	for i, want := range []int{maxTrafficEntriesPerReport, maxTrafficEntriesPerReport, 1} {
		reports := ts.link.trafficReports()
		if len(reports) != i+1 {
			t.Fatalf("第 %d 份：已发 %d 份（没 ack 不该发下一份）", i+1, len(reports))
		}
		cur := reports[i]
		if len(cur.GetEntries()) != want {
			t.Fatalf("第 %d 份 %d 条，期望 %d", i+1, len(cur.GetEntries()), want)
		}
		for _, e := range cur.GetEntries() {
			if seen[e.GetUid()] {
				t.Fatalf("uid %d 报了两次", e.GetUid())
			}
			seen[e.GetUid()] = true
		}
		ts.report()
		if again := ts.link.trafficReports(); len(again) != i+2 || again[i+1].GetReportId() != cur.GetReportId() {
			t.Fatalf("第 %d 份没确认前应原样重发同一份", i+1)
		}
		ts.link.mu.Lock()
		ts.link.traffic = ts.link.traffic[:i+1]
		ts.link.mu.Unlock()
		ts.handleEvent(panel.Event{Kind: panel.EventTrafficAck, ReportID: cur.GetReportId()})
	}
	if len(seen) != total || !ts.traffic.empty() {
		t.Fatalf("报了 %d 个用户，期望 %d；缓冲空 = %v", len(seen), total, ts.traffic.empty())
	}
}

// 在线地址按节点、uid 排序原样转发，超过单条上限分多条；没连着不报。
func TestOnlineReportedInChunks(t *testing.T) {
	ts, k := twoNodes(t)
	online := make(map[int64][]string, maxOnlineEntriesPerMessage+1)
	for i := range maxOnlineEntriesPerMessage + 1 {
		online[int64(i+1)] = []string{"192.0.2.1"}
	}
	online[999999] = nil
	k.setOnline("n1", online)
	k.setOnline("n2", map[int64][]string{7: {"2001:db8::"}})
	ts.report()
	ts.link.mu.Lock()
	msgs := ts.link.online
	ts.link.mu.Unlock()
	if len(msgs) != 2 || len(msgs[0].GetEntries()) != maxOnlineEntriesPerMessage || len(msgs[1].GetEntries()) != 2 {
		t.Fatalf("分条不对：%d 条", len(msgs))
	}
	if e := msgs[0].GetEntries()[0]; e.GetNodeId() != "n1" || e.GetUid() != 1 {
		t.Fatalf("排序不对：%v", e)
	}
	if e := msgs[1].GetEntries()[1]; e.GetNodeId() != "n2" || e.GetUid() != 7 {
		t.Fatalf("排序不对：%v", e)
	}
	ts.disconnect()
	ts.report()
	ts.link.mu.Lock()
	defer ts.link.mu.Unlock()
	if len(ts.link.online) != 2 {
		t.Fatal("没连着也报了在线地址")
	}
}

// 删节点前最后取一次流量，不丢。
func TestRemovedNodeTrafficCollected(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n2", trafficOf(1, 5, 6))
	ts.config(portConfig(t, "n1", "vless", 20001))
	ts.report()
	reports := ts.link.trafficReports()
	if len(reports) != 1 {
		t.Fatalf("报告 = %v", reports)
	}
	assertStrings(t, "条目", entriesOf(reports[0]), "n2/1:5/6")
}

// 退出：内核交出的最后一轮流量与缓冲一起报，等到面板确认。
func TestShutdownReportsDrainedTrafficUntilAcked(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n1", trafficOf(1, 10, 20))
	ts.report()
	first := ts.link.trafficReports()[0]

	go func() {
		ts.link.events <- panel.Event{Kind: panel.EventTrafficAck, ReportID: first.GetReportId()}
		for {
			time.Sleep(5 * time.Millisecond)
			reports := ts.link.trafficReports()
			if last := reports[len(reports)-1]; last.GetReportId() != first.GetReportId() {
				ts.link.events <- panel.Event{Kind: panel.EventTrafficAck, ReportID: last.GetReportId()}
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ts.Shutdown(ctx, map[string][]core.UserTraffic{"n2": {trafficOf(3, 1, 2)}}, true)
	if !ts.traffic.empty() {
		t.Fatal("确认后缓冲应清空")
	}
	reports := ts.link.trafficReports()
	assertStrings(t, "退出时报的", entriesOf(reports[len(reports)-1]), "n2/3:1/2")
}

// 退出时面板一直不确认：等到 ctx 到期就放弃，不卡住。
func TestShutdownGivesUpAtDeadline(t *testing.T) {
	ts, k := twoNodes(t)
	k.feed("n1", trafficOf(1, 10, 20))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ts.Shutdown(ctx, nil, false)
	if ts.traffic.empty() || len(ts.link.trafficReports()) != 1 {
		t.Fatal("没 drain 时应先取内核流量再报，并在到期后留着")
	}
}
