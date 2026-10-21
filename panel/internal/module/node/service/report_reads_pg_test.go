package service

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReportNodeCountsAndOfflinePG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	now := time.Now().UTC() // 心跳写的是库的 now()，不用服务钉住的时钟

	c0, err := e.svc.ReportNodeCounts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	o0, err := e.svc.OfflineNodes(ctx, now, 3)
	if err != nil {
		t.Fatal(err)
	}

	s1 := seedTrafficServer(t, ctx, e)
	s2 := seedTrafficServer(t, ctx, e)
	e.mustExec(t, ctx, `UPDATE servers SET last_heartbeat_at = now() WHERE id = ?`, s1)
	e.mustExec(t, ctx, `UPDATE servers SET last_heartbeat_at = now() - interval '10 minutes' WHERE id = ?`, s2)

	port1 := 30001
	port2 := 30002
	nActiveS1 := seedTrafficNode(t, ctx, e, &s1, "1.00")
	nActiveS2 := seedTrafficNode(t, ctx, e, &s2, "1.00")
	seedTrafficNode(t, ctx, e, &s1, "1.00") // S1 上的草稿节点：不计入 Total（只数服务中的节点），也不算在线、失联

	activateNode := func(nodeID uuid.UUID, port int) {
		e.mustExec(t, ctx, `UPDATE nodes SET serving_status = 'active', node_type = 'vless', server_host = 'node.example.test',
		server_port = ?, listen_l4 = 'tcp', protocol_schema_version = 1 WHERE id = ?`, port, nodeID)
	}
	activateNode(nActiveS1, port1)
	activateNode(nActiveS2, port2)

	c1, err := e.svc.ReportNodeCounts(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Total != c0.Total+2 {
		t.Fatalf("Total: want %d got %d", c0.Total+2, c1.Total)
	}
	if c1.Online != c0.Online+1 {
		t.Fatalf("Online: want %d got %d", c0.Online+1, c1.Online)
	}

	off, err := e.svc.OfflineNodes(ctx, now, 3)
	if err != nil {
		t.Fatal(err)
	}
	if off.Count != o0.Count+1 {
		t.Fatalf("Offline Count: want %d got %d", o0.Count+1, off.Count)
	}
	foundS2 := false
	for _, ref := range off.Sample {
		if ref.ID == nActiveS2 {
			foundS2 = true
			break
		}
	}
	if !foundS2 {
		t.Fatalf("Sample 应含 S2 上 active 节点 %s", nActiveS2)
	}
	if off.LongestOfflineSeconds == nil || *off.LongestOfflineSeconds < 590 {
		t.Fatalf("LongestOfflineSeconds: got %v", off.LongestOfflineSeconds)
	}
}

func TestCertAttentionNodesPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

	srv := seedTrafficServer(t, ctx, e)
	port := 30100

	activateWithCert := func(nodeID uuid.UUID, port int, identifier *string, notAfter *time.Time, errCode *string) {
		e.mustExec(t, ctx, `UPDATE nodes SET serving_status = 'active', node_type = 'vless', server_host = 'node.example.test',
			server_port = ?, listen_l4 = 'tcp', protocol_schema_version = 1 WHERE id = ?`, port, nodeID)
		if identifier == nil && notAfter == nil && errCode == nil {
			return
		}
		var detail *string
		if errCode != nil {
			detail = ptr("test detail")
		}
		// 证书列的 CHECK：任一有值时 cert_state_at 必须有值，detail 要跟着 code
		e.mustExec(t, ctx, `UPDATE nodes SET cert_identifier = CAST(? AS text), cert_not_after = CAST(? AS timestamptz),
			cert_error_code = CAST(? AS text), cert_error_detail = CAST(? AS text),
			cert_next_attempt_at = NULL, cert_state_at = now() WHERE id = ?`,
			identifier, notAfter, errCode, detail, nodeID)
	}

	// 失败
	nFail := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nFail, port, ptr("fail.example.test"), ptr(now.Add(30*24*time.Hour)), ptr("dns_failed"))
	port++

	// IP 证书 1 天后到期（应算 expiring）
	nIP1d := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nIP1d, port, ptr("203.0.113.10"), ptr(now.Add(24*time.Hour)), nil)
	port++

	// 域名证书 10 天后到期（应算 expiring）
	nDom10 := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nDom10, port, ptr("soon.example.test"), ptr(now.Add(10*24*time.Hour)), nil)
	port++

	// 域名证书 30 天后到期（不算）
	nDom30 := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nDom30, port, ptr("ok.example.test"), ptr(now.Add(30*24*time.Hour)), nil)
	port++

	// IP 证书 5 天后到期（不算，门槛 2 天）
	nIP5d := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nIP5d, port, ptr("203.0.113.20"), ptr(now.Add(5*24*time.Hour)), nil)
	port++

	// 非 active 节点有错（不算）
	nDis := seedTrafficNode(t, ctx, e, &srv, "1.00")
	activateWithCert(nDis, port, ptr("disabled.example.test"), ptr(now.Add(24*time.Hour)), ptr("rate_limited"))
	e.mustExec(t, ctx, `UPDATE nodes SET serving_status = 'disabled' WHERE id = ?`, nDis)

	out, err := e.svc.CertAttentionNodes(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 3 {
		t.Fatalf("Count: want 3 got %d", out.Count)
	}
	if out.Failing != 1 {
		t.Fatalf("Failing: want 1 got %d", out.Failing)
	}
	if out.Expiring != 2 {
		t.Fatalf("Expiring: want 2 got %d", out.Expiring)
	}
	wantOrder := []uuid.UUID{nIP1d, nDom10, nFail}
	if len(out.Sample) != 3 {
		t.Fatalf("Sample len: want 3 got %d", len(out.Sample))
	}
	for i, id := range wantOrder {
		if out.Sample[i].ID != id {
			t.Fatalf("Sample[%d].ID: want %s got %s", i, id, out.Sample[i].ID)
		}
	}
}

func TestTrafficReadsPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	from := e.hour.Add(-time.Hour)
	to := e.hour.Add(time.Hour)

	srv := seedTrafficServer(t, ctx, e)
	n := seedTrafficNode(t, ctx, e, &srv, "1.00")
	subA, _ := seedTrafficSub(t, ctx, e)
	subB, _ := seedTrafficSub(t, ctx, e)

	e.mustExec(t, ctx, `INSERT INTO node_traffic_hourly (node_id, hour_start, upload_bytes, download_bytes)
		VALUES (?, ?, 100, 200)`, n, e.hour)
	e.mustExec(t, ctx, `INSERT INTO node_traffic_hourly (node_id, hour_start, upload_bytes, download_bytes)
		VALUES (?, ?, 999, 0)`, n, e.hour.Add(-3*time.Hour))

	e.mustExec(t, ctx, `INSERT INTO node_user_traffic_hourly (hour_start, node_id, subscription_id, upload_bytes, download_bytes)
		VALUES (?, ?, ?, 10, 20)`, e.hour, n, subA)
	e.mustExec(t, ctx, `INSERT INTO node_user_traffic_hourly (hour_start, node_id, subscription_id, upload_bytes, download_bytes)
		VALUES (?, ?, ?, 0, 0)`, e.hour, n, subB)
	e.mustExec(t, ctx, `INSERT INTO node_user_traffic_hourly (hour_start, node_id, subscription_id, upload_bytes, download_bytes)
		VALUES (?, ?, ?, 5, 5)`, e.hour.Add(-time.Hour), n, subA)

	sha := make([]byte, 32)
	if _, err := rand.Read(sha); err != nil {
		t.Fatal(err)
	}
	rid := poolRandName(t, "rep")
	e.mustExec(t, ctx, `INSERT INTO node_traffic_reports (server_id, report_id, entries, content_sha256, entry_count,
		upload_bytes, download_bytes, invalid_count, unlisted_count, received_at)
		VALUES (?, ?, '[]'::jsonb, ?, 3, 0, 0, 2, 0, ?)`, srv, rid, sha, e.hour)

	byNode, err := e.svc.TrafficByNode(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var nt *NodeTraffic
	for i := range byNode {
		if byNode[i].NodeID == n {
			nt = &byNode[i]
			break
		}
	}
	if nt == nil {
		t.Fatal("TrafficByNode 应含节点 N")
	}
	if nt.Upload != 100 || nt.Download != 200 || nt.ActiveHours != 1 || nt.Cells != 2 {
		t.Fatalf("节点流量: up=%d down=%d hours=%d cells=%d", nt.Upload, nt.Download, nt.ActiveHours, nt.Cells)
	}

	bySub, err := e.svc.TrafficBySubscription(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var st *SubscriptionTraffic
	for i := range bySub {
		if bySub[i].SubscriptionID == subA {
			st = &bySub[i]
		}
		if bySub[i].SubscriptionID == subB {
			t.Fatal("TrafficBySubscription 不应含 B（合计为 0）")
		}
	}
	if st == nil {
		t.Fatal("TrafficBySubscription 应含 A")
	}
	if st.Upload != 15 || st.Download != 25 || st.Cells != 2 {
		t.Fatalf("订阅 A: up=%d down=%d cells=%d", st.Upload, st.Download, st.Cells)
	}

	q, err := e.svc.TrafficQuality(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if q.InvalidReports < 1 || q.InvalidEntries < 2 {
		t.Fatalf("TrafficQuality: reports=%d entries=%d", q.InvalidReports, q.InvalidEntries)
	}
}
