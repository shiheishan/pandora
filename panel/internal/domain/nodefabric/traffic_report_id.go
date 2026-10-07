package nodefabric

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// TrafficReportIDHeader 是流量上报的幂等键（pdnd panel/client.go 的 ReportIDHeader）。
// 放在请求头而不是报文里：报文是 {"<uid>": [up, down]}，加任何非 uid 的键都会让别的面板解析失败。
const TrafficReportIDHeader = "X-Report-Id"

// maxTrafficReportIDBytes 与迁移 00123 的 CHECK 一致；pdnd 发的是 36 字节的 UUID。
const maxTrafficReportIDBytes = 64

// NormalizeTrafficReportID 校验节点给的上报编号：1–64 个 [A-Za-z0-9._:-]。不合规（或没带）
// 返回空串与 ok=false，调用方按老节点处理（10 秒内容哈希去重）：拒收整份会让 pdnd 把这份
// 流量当作被拒收而丢弃，计费上比退回近似去重更糟。
func NormalizeTrafficReportID(raw string) (id string, ok bool) {
	if raw == "" || len(raw) > maxTrafficReportIDBytes {
		return "", false
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == ':' || c == '-') {
			return "", false
		}
	}
	return raw, true
}

// errTrafficOriginalMissing 是「同一编号的第一份已冲突、却查不到它」：只可能是第一份刚被清理。
// 整笔回滚，节点下一轮带同一个编号重发。
var errTrafficOriginalMissing = errors.New("traffic report original vanished after conflict")

// recordTrafficReport 写留档并判重复，返回留档行 id 与是否重复件。
//
// 带编号：INSERT … ON CONFLICT (node_id, client_report_id) DO NOTHING。同一编号的第一份
// 还在别的事务里没提交时，这里等它提交再判冲突（00123 的唯一部分索引），所以并发的两份
// 只有一份入账。冲突了就再写一行重复件：编号列留空，duplicate_of 指向第一份。正常路径仍是
// 一条语句、一次往返；只有重复件多一次。
//
// 不带编号：留档与近似去重合成一条语句，同一节点 10 秒内的同一报文记为重复（原有口径）。
func recordTrafficReport(ctx context.Context, tx pgx.Tx, tenantID string, n *ServingNode,
	report trafficReport, raw, sum []byte, reportID string) (rowID string, duplicate bool, err error) {
	if reportID == "" {
		err = tx.QueryRow(ctx, `
			INSERT INTO node_traffic_reports
				(tenant_id, node_id, user_count, total_upload, total_download,
				 traffic_rate, raw_payload, content_hash, duplicate_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
				(SELECT d.id FROM node_traffic_reports d
				  WHERE d.node_id = $2 AND d.content_hash = $8
				    AND d.received_at > now() - interval '10 seconds'
				  ORDER BY d.received_at DESC LIMIT 1))
			RETURNING id, duplicate_of IS NOT NULL`,
			tenantID, n.ID, report.keys, report.upload, report.download,
			n.TrafficRate, raw, sum,
		).Scan(&rowID, &duplicate)
		return rowID, duplicate, err
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO node_traffic_reports
			(tenant_id, node_id, user_count, total_upload, total_download,
			 traffic_rate, raw_payload, content_hash, client_report_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (node_id, client_report_id) WHERE client_report_id IS NOT NULL DO NOTHING
		RETURNING id`,
		tenantID, n.ID, report.keys, report.upload, report.download,
		n.TrafficRate, raw, sum, reportID,
	).Scan(&rowID)
	if err == nil {
		return rowID, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, err
	}
	// 同一编号已经入账：只留档一行重复件。新语句的快照看得见刚提交的第一份。
	var linked bool
	err = tx.QueryRow(ctx, `
		INSERT INTO node_traffic_reports
			(tenant_id, node_id, user_count, total_upload, total_download,
			 traffic_rate, raw_payload, content_hash, duplicate_of)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,
			(SELECT d.id FROM node_traffic_reports d
			  WHERE d.node_id = $2 AND d.client_report_id = $9))
		RETURNING id, duplicate_of IS NOT NULL`,
		tenantID, n.ID, report.keys, report.upload, report.download,
		n.TrafficRate, raw, sum, reportID,
	).Scan(&rowID, &linked)
	if err != nil {
		return "", false, err
	}
	if !linked {
		return "", false, errTrafficOriginalMissing
	}
	return rowID, true, nil
}
