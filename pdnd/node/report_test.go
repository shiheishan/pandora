package node

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// trafficCore 在 userTableCore 之上按队列吐流量（取出即清零，与真内核一致）。
type trafficCore struct {
	*userTableCore
	mu    sync.Mutex
	queue []core.UserTraffic
}

func (c *trafficCore) feed(t ...core.UserTraffic) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = append(c.queue, t...)
}

func (c *trafficCore) GetTraffic(string) ([]core.UserTraffic, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.queue
	c.queue = nil
	return out, nil
}

// pushPanel 记下每一次流量上报（report_id 与报文），按脚本回状态码。
type pushPanel struct {
	mu     sync.Mutex
	codes  []int // 第 i 次上报回的状态码，超出即 200
	pushes []pushRecord
}

type pushRecord struct {
	id      string
	payload map[string][2]int64
	code    int
}

func (p *pushPanel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/server/UniProxy/push" {
		w.WriteHeader(http.StatusOK)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var payload map[string][2]int64
	_ = json.Unmarshal(raw, &payload)
	p.mu.Lock()
	defer p.mu.Unlock()
	code := http.StatusOK
	if i := len(p.pushes); i < len(p.codes) {
		code = p.codes[i]
	}
	p.pushes = append(p.pushes, pushRecord{id: r.Header.Get(panel.ReportIDHeader), payload: payload, code: code})
	w.WriteHeader(code)
}

func newReportFixture(t *testing.T, codes ...int) (*Node, *trafficCore, *pushPanel) {
	t.Helper()
	fake := &pushPanel{codes: codes}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	kernel := &trafficCore{userTableCore: newUserTableCore()}
	client := panel.New(panel.Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "token"})
	n := New(client, kernel, testLogger())
	n.started = true
	return n, kernel, fake
}

// 上报失败（面板 503）不丢流量：下一轮原样重发同一份、带同一个 report_id
// （面板据此去重），这期间的新流量按 uid 合并，在同一轮紧接着报。
func TestTrafficRetriedWithSameReportIDAndMerged(t *testing.T) {
	n, kernel, fake := newReportFixture(t, http.StatusServiceUnavailable)
	ctx := context.Background()

	kernel.feed(core.UserTraffic{ID: 1, Upload: 100, Download: 200})
	n.report(ctx) // 503
	kernel.feed(core.UserTraffic{ID: 1, Upload: 10, Download: 20}, core.UserTraffic{ID: 2, Upload: 1, Download: 2})
	kernel.feed(core.UserTraffic{ID: 2, Upload: 3, Download: 4})
	n.report(ctx) // 重发 + 新的一份

	if len(fake.pushes) != 3 {
		t.Fatalf("上报 %d 次，期望 3 次（失败一次、原样重发一次、新流量一次）", len(fake.pushes))
	}
	first, retry, next := fake.pushes[0], fake.pushes[1], fake.pushes[2]
	if first.id == "" || retry.id != first.id {
		t.Fatalf("重发没有带同一个 report_id：%q / %q", first.id, retry.id)
	}
	if retry.payload["1"] != [2]int64{100, 200} || len(retry.payload) != 1 {
		t.Fatalf("重发的报文变了：%v", retry.payload)
	}
	if next.id == first.id || next.id == "" {
		t.Fatalf("新的一份应当有新的 report_id：%q", next.id)
	}
	if next.payload["1"] != [2]int64{10, 20} || next.payload["2"] != [2]int64{4, 6} {
		t.Fatalf("新流量没有按 uid 合并：%v", next.payload)
	}
	if !n.traffic.empty() {
		t.Fatal("全部收下之后缓冲没清空")
	}
}

// 面板明确拒收（400）的那一份丢弃并计数，不能挡住后面的上报。
func TestTrafficRejectedReportIsDroppedAndCounted(t *testing.T) {
	n, kernel, fake := newReportFixture(t, http.StatusBadRequest)
	ctx := context.Background()
	kernel.feed(core.UserTraffic{ID: 1, Upload: 5, Download: 5})
	n.report(ctx)
	if n.traffic.droppedReports != 1 || n.traffic.droppedBytes != 10 || !n.traffic.empty() {
		t.Fatalf("拒收的一份没有丢弃计数：%+v", n.traffic)
	}
	kernel.feed(core.UserTraffic{ID: 1, Upload: 1, Download: 1})
	n.report(ctx)
	if len(fake.pushes) != 2 || fake.pushes[1].code != http.StatusOK {
		t.Fatalf("拒收之后的上报被挡住了：%+v", fake.pushes)
	}
}

// 缓冲有上限：超限先丢最旧的那一份（inflight），并计数。
func TestTrafficBufferCapDropsOldest(t *testing.T) {
	var b trafficBuffer
	b.add([]core.UserTraffic{{ID: 1, Upload: 7, Download: 7}})
	b.seal() // 成为 inflight（最旧的一份）
	big := make([]core.UserTraffic, 0, maxBufferedTrafficUsers)
	for i := 0; i < maxBufferedTrafficUsers; i++ {
		big = append(big, core.UserTraffic{ID: int64(i + 100), Upload: 1})
	}
	dropped := b.add(big)
	if dropped != 14 || b.inflight != nil || b.droppedReports != 1 {
		t.Fatalf("超限没有丢最旧的一份：dropped=%d inflight=%v reports=%d", dropped, b.inflight, b.droppedReports)
	}
	if b.size() != maxBufferedTrafficUsers {
		t.Fatalf("缓冲条目 %d，期望 %d", b.size(), maxBufferedTrafficUsers)
	}
	if more := b.add([]core.UserTraffic{{ID: 999999, Upload: 3}}); more != 3 || b.size() != maxBufferedTrafficUsers {
		t.Fatalf("没有 inflight 时超限的新条目应丢弃计数：dropped=%d size=%d", more, b.size())
	}
}

// 退出：内核关停后交出的最后一轮流量与积压的待报流量一起报。
func TestShutdownReportsDrainedAndBufferedTraffic(t *testing.T) {
	n, kernel, fake := newReportFixture(t, http.StatusServiceUnavailable)
	ctx := context.Background()
	kernel.feed(core.UserTraffic{ID: 1, Upload: 100, Download: 100})
	n.report(ctx) // 503，积压
	n.Shutdown(ctx, []core.UserTraffic{{ID: 1, Upload: 1, Download: 2}, {ID: 3, Upload: 5, Download: 5}}, true)

	var total int64
	for _, p := range fake.pushes {
		if p.code != http.StatusOK {
			continue
		}
		for _, v := range p.payload {
			total += v[0] + v[1]
		}
	}
	if total != 213 {
		t.Fatalf("退出时交给面板 %d 字节，期望 213（积压 200 + 关停交出 13）", total)
	}
	if !n.traffic.empty() {
		t.Fatal("退出后缓冲里还有没报的流量")
	}
}
