package service

// IngestTraffic 的 PostgreSQL 集成测试：留档、折算、名单外与不合规、重放、冲突、小时汇总。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// trafficFixture 是一台服务器 S 上的 N1（倍率 1.00）、N2（倍率 1.50），另一台服务器上的 NX，两份订阅 A、B。
// 名单含 (N1,A)、(N2,A)、(N1,B)，不含 (N2,B)。
type trafficFixture struct {
	server, n1, n2, nx uuid.UUID
	subA, subB         uuid.UUID
	uidA, uidB         int64
	members            fakeMembers
}

func newTrafficFixture(t *testing.T, ctx context.Context, e *trafficEnv) trafficFixture {
	t.Helper()
	var f trafficFixture
	f.server = seedTrafficServer(t, ctx, e)
	other := seedTrafficServer(t, ctx, e)
	f.n1 = seedTrafficNode(t, ctx, e, &f.server, "1.00")
	f.n2 = seedTrafficNode(t, ctx, e, &f.server, "1.50")
	f.nx = seedTrafficNode(t, ctx, e, &other, "1.00")
	f.subA, f.uidA = seedTrafficSub(t, ctx, e)
	f.subB, f.uidB = seedTrafficSub(t, ctx, e)
	f.members = fakeMembers{{f.n1, f.uidA}: true, {f.n2, f.uidA}: true, {f.n1, f.uidB}: true}
	return f
}

// mixedReport 覆盖入账、折算、名单外、别的服务器、uid 非正、超上限。
func (f trafficFixture) mixedReport(id string) TrafficReport {
	return TrafficReport{ReportID: id, Entries: []TrafficEntry{
		{NodeID: f.n1, UID: f.uidA, Upload: 100, Download: 200},
		{NodeID: f.n2, UID: f.uidA, Upload: 1, Download: 2},
		{NodeID: f.n2, UID: f.uidB, Upload: 10, Download: 0},
		{NodeID: f.nx, UID: f.uidA, Upload: 5, Download: 5},
		{NodeID: f.n1, UID: 0, Upload: 1, Download: 1},
		{NodeID: f.n1, UID: f.uidB, Upload: maxTrafficEntryBytes + 1, Download: 0},
	}}
}

// nodeHour 读一个节点当前整点的上下行与计费（没有行三项都是 -1）。
func nodeHour(t *testing.T, ctx context.Context, e *trafficEnv, nodeID uuid.UUID, hour time.Time) (up, down, billed int64) {
	t.Helper()
	up, down, billed = -1, -1, -1
	_ = e.row(ctx, `SELECT upload_bytes, download_bytes, billed_bytes FROM node_traffic_hourly WHERE node_id = ? AND hour_start = ?`,
		nodeID, hour).Scan(&up, &down, &billed)
	return
}

func userHourBilled(t *testing.T, ctx context.Context, e *trafficEnv, nodeID, subID uuid.UUID, hour time.Time) int64 {
	t.Helper()
	var billed int64 = -1
	_ = e.row(ctx, `SELECT billed_bytes FROM node_user_traffic_hourly WHERE node_id = ? AND subscription_id = ? AND hour_start = ?`,
		nodeID, subID, hour).Scan(&billed)
	return billed
}

func TestIngestTrafficChargesAndArchivesPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	hour := e.hour

	if err := e.svc.IngestTraffic(ctx, f.server, f.mixedReport("r-1"), f.members); err != nil {
		t.Fatalf("IngestTraffic: %v", err)
	}

	var entries, invalid, unlisted, up, down int64
	if err := e.row(ctx, `SELECT entry_count, invalid_count, unlisted_count, upload_bytes, download_bytes
		FROM node_traffic_reports WHERE server_id = ? AND report_id = 'r-1'`, f.server).Scan(&entries, &invalid, &unlisted, &up, &down); err != nil {
		t.Fatal(err)
	}
	if entries != 6 || invalid != 3 || unlisted != 1 || up != 111 || down != 202 {
		t.Fatalf("留档计数 entries=%d invalid=%d unlisted=%d up=%d down=%d", entries, invalid, unlisted, up, down)
	}

	if e.charger.callCount() != 1 {
		t.Fatalf("应扣量 1 次，得 %d", e.charger.callCount())
	}
	got := map[int64]int64{}
	for _, c := range e.charger.calls[0] {
		if c.UID == f.uidB {
			t.Fatalf("名单外与不合规的 B 不该扣：%v", e.charger.calls[0])
		}
		got[c.UID] += c.Bytes
	}
	if got[f.uidA] != 300+4 {
		t.Fatalf("A 应扣 300 + 4（3 字节 × 1.5 截断），得 %d", got[f.uidA])
	}

	if u, d, b := nodeHour(t, ctx, e, f.n1, hour); u != 100 || d != 200 || b != 300 {
		t.Fatalf("N1 小时汇总 %d/%d/%d", u, d, b)
	}
	if u, d, b := nodeHour(t, ctx, e, f.n2, hour); u != 11 || d != 2 || b != 4 {
		t.Fatalf("N2 小时汇总 %d/%d/%d（名单外的字节计入实际流量，不计费）", u, d, b)
	}
	if u, _, _ := nodeHour(t, ctx, e, f.nx, hour); u != -1 {
		t.Fatal("别的服务器的节点不该有汇总")
	}
	if b := userHourBilled(t, ctx, e, f.n1, f.subA, hour); b != 300 {
		t.Fatalf("(N1,A) billed %d", b)
	}
	if b := userHourBilled(t, ctx, e, f.n2, f.subA, hour); b != 4 {
		t.Fatalf("(N2,A) billed %d", b)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM node_user_traffic_hourly WHERE subscription_id = ?`, f.subB); n != 0 {
		t.Fatalf("B 不该有用户小时行，得 %d", n)
	}

	// 同一编号、同样内容（换了顺序）重放：不再扣、不再累加
	r := f.mixedReport("r-1")
	r.Entries[0], r.Entries[5] = r.Entries[5], r.Entries[0]
	if err := e.svc.IngestTraffic(ctx, f.server, r, f.members); err != nil {
		t.Fatalf("重放: %v", err)
	}
	if e.charger.callCount() != 1 {
		t.Fatalf("重放不该再扣量，扣了 %d 次", e.charger.callCount())
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM node_traffic_reports WHERE server_id = ?`, f.server); n != 1 {
		t.Fatalf("留档应仍是 1 行，得 %d", n)
	}
	if _, _, b := nodeHour(t, ctx, e, f.n1, hour); b != 300 {
		t.Fatalf("重放不该累加小时汇总，得 %d", b)
	}

	// 同一编号、内容不同：冲突，不入账（不出 SQL 错误，事务仍可用）
	r = f.mixedReport("r-1")
	r.Entries[0].Download++
	if err := e.svc.IngestTraffic(ctx, f.server, r, f.members); !errors.Is(err, ErrTrafficReportConflict) {
		t.Fatalf("内容不同应报冲突，得 %v", err)
	}
	if e.charger.callCount() != 1 {
		t.Fatal("冲突不该扣量")
	}
}

func TestIngestTrafficInvalidReportIDPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	if err := e.svc.IngestTraffic(ctx, f.server, f.mixedReport("bad id"), f.members); !errors.Is(err, ErrTrafficReportInvalid) {
		t.Fatalf("编号不合规应报错，得 %v", err)
	}
	if n := e.count(t, ctx, `SELECT count(*) FROM node_traffic_reports WHERE server_id = ?`, f.server); n != 0 {
		t.Fatalf("不该留档，得 %d", n)
	}
}

func TestIngestTrafficHourlyAccumulatesPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	hour := e.hour
	for _, id := range []string{"a-1", "a-2"} {
		r := TrafficReport{ReportID: id, Entries: []TrafficEntry{{NodeID: f.n1, UID: f.uidA, Upload: 4, Download: 6}}}
		if err := e.svc.IngestTraffic(ctx, f.server, r, f.members); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if u, d, b := nodeHour(t, ctx, e, f.n1, hour); u != 8 || d != 12 || b != 20 {
		t.Fatalf("两份上报应累加：%d/%d/%d", u, d, b)
	}
	if b := userHourBilled(t, ctx, e, f.n1, f.subA, hour); b != 20 {
		t.Fatalf("用户小时行应累加，得 %d", b)
	}
}

func TestIngestTrafficZeroRateNotBilledPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	free := seedTrafficNode(t, ctx, e, &f.server, "0")
	f.members[trafficKey{free, f.uidA}] = true
	r := TrafficReport{ReportID: "z-1", Entries: []TrafficEntry{{NodeID: free, UID: f.uidA, Upload: 500, Download: 500}}}
	if err := e.svc.IngestTraffic(ctx, f.server, r, f.members); err != nil {
		t.Fatal(err)
	}
	if c := e.charger.calls[0]; len(c) != 1 || c[0].Bytes != 0 {
		t.Fatalf("0 倍率应计 0 字节（仍给出 uid 以记用户小时行）：%v", c)
	}
	hour := e.hour
	if u, _, b := nodeHour(t, ctx, e, free, hour); u != 500 || b != 0 {
		t.Fatalf("0 倍率节点 %d/%d", u, b)
	}
}
