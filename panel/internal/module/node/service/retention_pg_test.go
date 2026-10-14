package service

// 节点数据保留期清理的 PostgreSQL 集成测试与未知表单元测试。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// 安装令牌保留期：没用过、过期或作废满 30 天的删；没满 30 天的、用过的、被身份引用的都留着；
// 绕过任务直接 DELETE 用过的或没满期的令牌，仍被 bootstrap_tokens_guard（00037）拒绝。
func TestPurgeBootstrapTokensPG(t *testing.T) {
	e, ctx := newTrafficEnv(t)
	f := newTrafficFixture(t, ctx, e)

	// seedToken 插一张令牌：createdAgo 是签发距今（库时钟），有效期 30 分钟；
	// used 时 5 分钟后被用掉，voided 时 10 分钟后被作废。
	seedToken := func(createdAgo string, used, voided bool) uuid.UUID {
		t.Helper()
		usedAt, usedReq, voidedAt := "NULL::timestamptz", "NULL::text", "NULL::timestamptz"
		if used {
			usedAt, usedReq = "c.at + interval '5 minutes'", "'"+poolRandName(t, "req-")+"'"
		}
		if voided {
			voidedAt = "c.at + interval '10 minutes'"
		}
		var id uuid.UUID
		if err := e.row(ctx, `INSERT INTO bootstrap_tokens
				(server_id, token_sha256, created_at, expires_at, used_at, used_request_id, voided_at)
			SELECT CAST(? AS uuid), encode(sha256(convert_to(CAST(? AS text), 'UTF8')), 'hex'), c.at, c.at + interval '30 minutes', `+
			usedAt+`, `+usedReq+`, `+voidedAt+`
			  FROM (SELECT now() - interval '`+createdAgo+`' AS at) c
			RETURNING id`, f.server, poolRandName(t, "tok-")).Scan(&id); err != nil {
			t.Fatalf("插令牌: %v", err)
		}
		return id
	}
	// seedIdentity 插一份引用 tokenID 的接入身份（已被取代，不占 active 唯一索引）。
	seedIdentity := func(seq int, tokenID uuid.UUID) {
		t.Helper()
		e.mustExec(t, ctx, `INSERT INTO server_identities
				(server_id, seq, cert_serial, spki_sha256, cert_pem, source, enroll_token_id, enroll_request_id,
				 status, activated_at, issued_at, expires_at, superseded_at)
			VALUES (?, ?, 'a' || substr(encode(sha256(convert_to(?, 'UTF8')), 'hex'), 1, 30),
			        encode(sha256(convert_to(?, 'UTF8')), 'hex'), '-----BEGIN CERTIFICATE-----虚构',
			        'enroll', ?, ?, 'superseded', now() - interval '40 days', now() - interval '40 days',
			        now() + interval '50 days', now() - interval '39 days')`,
			f.server, seq, poolRandName(t, "serial-"), poolRandName(t, "spki-"), tokenID, poolRandName(t, "req-"))
	}

	oldUnused := seedToken("40 days", false, false)
	oldVoided := seedToken("40 days", false, true)
	recentUnused := seedToken("10 days", false, false)
	oldUsed := seedToken("40 days", true, false)
	seedIdentity(1, oldUsed)
	oldReferenced := seedToken("40 days", false, false)
	seedIdentity(2, oldReferenced)

	n, err := e.svc.PurgeRetention(ctx, "bootstrap_tokens", time.Now(), 1000)
	if err != nil {
		t.Fatalf("PurgeRetention: %v", err)
	}
	if n < 2 {
		t.Fatalf("至少应删 2 行（过期、作废满 30 天的未用令牌），得 %d", n)
	}
	exists := func(id uuid.UUID) bool {
		return e.count(t, ctx, `SELECT count(*) FROM bootstrap_tokens WHERE id = ?`, id) == 1
	}
	for _, c := range []struct {
		what string
		id   uuid.UUID
		keep bool
	}{
		{"过期满 30 天的未用令牌", oldUnused, false},
		{"作废满 30 天的未用令牌", oldVoided, false},
		{"没满 30 天的未用令牌", recentUnused, true},
		{"用过的令牌", oldUsed, true},
		{"被身份引用的令牌", oldReferenced, true},
	} {
		if got := exists(c.id); got != c.keep {
			t.Fatalf("%s：还在 = %v，期望 %v", c.what, got, c.keep)
		}
	}

	// 绕过任务直接删：用过的、没满 30 天的都被触发器拒绝（不是外键）。
	for _, c := range []struct {
		what string
		id   uuid.UUID
	}{{"用过的令牌", oldUsed}, {"没满 30 天的未用令牌", recentUnused}} {
		e.mustExec(t, ctx, `SAVEPOINT bt_sp`)
		err := e.exec(ctx, `DELETE FROM bootstrap_tokens WHERE id = ?`, c.id)
		e.mustExec(t, ctx, `ROLLBACK TO SAVEPOINT bt_sp`)
		if err == nil || !strings.Contains(err.Error(), "只允许删除过期或作废满 30 天的未用令牌") {
			t.Fatalf("直接删%s应被触发器拒绝，得到 %v", c.what, err)
		}
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
