package service

// 节点数据保留期清理的 PostgreSQL 集成测试与未知表单元测试。

import (
	"context"
	"testing"
	"time"
)

func TestPurgeRetentionRawPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	zeroHash := "0000000000000000000000000000000000000000000000000000000000000000"
	ipOld, ipNew := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cases := []struct {
		table string
		seed  func(old bool)
		count func() int64
	}{
		{
			table: "node_traffic_reports",
			seed: func(old bool) {
				at := "now() - interval '40 days'"
				if !old {
					at = "now() - interval '10 days'"
				}
				rid := poolRandName(t, "r-old")
				if !old {
					rid = poolRandName(t, "r-new")
				}
				e.mustExec(t, ctx, `INSERT INTO node_traffic_reports
					(server_id, report_id, received_at, entries, content_sha256, entry_count,
					 upload_bytes, download_bytes, invalid_count, unlisted_count)
				VALUES (?, ?, `+at+`, '[]'::jsonb, decode(?, 'hex'), 0, 0, 0, 0, 0)`,
					f.server, rid, zeroHash)
			},
			count: func() int64 {
				return e.count(t, ctx, `SELECT count(*) FROM node_traffic_reports WHERE server_id = ?`, f.server)
			},
		},
		{
			table: "node_metrics",
			seed: func(old bool) {
				at := "now() - interval '40 days'"
				if !old {
					at = "now() - interval '10 days'"
				}
				e.mustExec(t, ctx, `INSERT INTO node_metrics
					(server_id, recorded_at, cpu_bp, mem_used_bytes, mem_total_bytes, disk_used_bytes, disk_total_bytes,
					 load1_cp, load5_cp, load15_cp, net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
				VALUES (?, `+at+`, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0)`, f.server)
			},
			count: func() int64 {
				return e.count(t, ctx, `SELECT count(*) FROM node_metrics WHERE server_id = ?`, f.server)
			},
		},
		{
			table: "node_alive_ips",
			seed: func(old bool) {
				at := "now() - interval '40 days'"
				hash := ipOld
				if !old {
					at = "now() - interval '10 days'"
					hash = ipNew
				}
				e.mustExec(t, ctx, `INSERT INTO node_alive_ips (node_id, subscription_id, ip_hash, last_seen_at)
					VALUES (?, ?, decode(?, 'hex'), `+at+`)`, f.n1, f.subA, hash)
			},
			count: func() int64 {
				return e.count(t, ctx, `SELECT count(*) FROM node_alive_ips WHERE node_id = ? AND subscription_id = ?`, f.n1, f.subA)
			},
		},
	}

	for _, c := range cases {
		c.seed(true)
		c.seed(false)
		if c.count() != 2 {
			t.Fatalf("%s 种子后应有 2 行，得 %d", c.table, c.count())
		}
		n, err := e.svc.PurgeRetention(ctx, c.table, time.Now(), 1000)
		if err != nil {
			t.Fatalf("%s PurgeRetention: %v", c.table, err)
		}
		if n != 1 {
			t.Fatalf("%s 应删 1 行，得 %d", c.table, n)
		}
		if c.count() != 1 {
			t.Fatalf("%s 清理后应剩 1 行，得 %d", c.table, c.count())
		}
	}
}

func TestPurgeRetentionRollupPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	for _, c := range []struct {
		table string
		seed  func(old bool)
		count func() int64
	}{
		{
			table: "node_traffic_hourly",
			seed: func(old bool) {
				interval := "420 days"
				if !old {
					interval = "30 days"
				}
				e.mustExec(t, ctx, `INSERT INTO node_traffic_hourly (node_id, hour_start, upload_bytes, download_bytes, billed_bytes)
					VALUES (?, date_trunc('hour', now()) - interval '`+interval+`', 1, 1, 0)`, f.n1)
			},
			count: func() int64 {
				return e.count(t, ctx, `SELECT count(*) FROM node_traffic_hourly WHERE node_id = ?`, f.n1)
			},
		},
		{
			table: "node_user_traffic_hourly",
			seed: func(old bool) {
				interval := "420 days"
				if !old {
					interval = "30 days"
				}
				e.mustExec(t, ctx, `INSERT INTO node_user_traffic_hourly (hour_start, node_id, subscription_id, upload_bytes, download_bytes, billed_bytes)
					VALUES (date_trunc('hour', now()) - interval '`+interval+`', ?, ?, 1, 1, 0)`, f.n1, f.subA)
			},
			count: func() int64 {
				return e.count(t, ctx, `SELECT count(*) FROM node_user_traffic_hourly WHERE node_id = ? AND subscription_id = ?`, f.n1, f.subA)
			},
		},
	} {
		c.seed(true)
		c.seed(false)
		if c.count() != 2 {
			t.Fatalf("%s 种子后应有 2 行，得 %d", c.table, c.count())
		}
		n, err := e.svc.PurgeRetention(ctx, c.table, time.Now(), 1000)
		if err != nil {
			t.Fatalf("%s PurgeRetention: %v", c.table, err)
		}
		if n != 1 {
			t.Fatalf("%s 应删 1 行，得 %d", c.table, n)
		}
		if c.count() != 1 {
			t.Fatalf("%s 清理后应剩 1 行，得 %d", c.table, c.count())
		}
	}
}

func TestPurgeRetentionFloorPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)
	e.mustExec(t, ctx, `INSERT INTO node_metrics
		(server_id, recorded_at, cpu_bp, mem_used_bytes, mem_total_bytes, disk_used_bytes, disk_total_bytes,
		 load1_cp, load5_cp, load15_cp, net_rx_bytes, net_tx_bytes, tcp_conns, uptime_sec)
	VALUES (?, now() - interval '10 days', 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0, 0)`, f.server)

	before := e.count(t, ctx, `SELECT count(*) FROM node_metrics WHERE recorded_at < now() - interval '31 days'`)
	var n int
	if err := e.row(ctx, `SELECT purge_node_metrics(now(), 10000)`).Scan(&n); err != nil {
		t.Fatalf("purge_node_metrics: %v", err)
	}
	if n != int(before) {
		t.Fatalf("purge_node_metrics 删了 %d 行，想要 %d（只该删 31 天以前的）", n, before)
	}
	if kept := e.count(t, ctx, `SELECT count(*) FROM node_metrics WHERE server_id = ?`, f.server); kept != 1 {
		t.Fatalf("10 天前的指标应剩 1 行，得 %d", kept)
	}

	badCtx := ctx
	if err := e.row(badCtx, `SELECT purge_node_traffic_reports(now(), 0)`).Scan(&n); err == nil {
		t.Fatal("purge_node_traffic_reports 每批 0 行应报错")
	}
}

func TestPurgeRetentionUnknownTable(t *testing.T) {
	_, err := (&Service{}).PurgeRetention(context.Background(), "no_such_table", time.Now(), 100)
	if err == nil {
		t.Fatal("未知表应报错")
	}
	if err.Error() != "未知的保留期清理表: no_such_table" {
		t.Fatalf("错误文案 %q", err.Error())
	}
}
