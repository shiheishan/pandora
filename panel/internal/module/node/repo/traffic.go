package repo

// 流量上报入账：留档、节点倍率、两张小时汇总。只给 service 的 IngestTraffic 用，都在它的事务里执行。

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/db"
)

// TrafficReportRow 是一份要留档的流量上报。
type TrafficReportRow struct {
	ServerID      uuid.UUID
	ReportID      string
	ReceivedAt    time.Time
	EntriesJSON   string // JSON 数组
	ContentSHA256 string // 小写 hex（64 字符）
	EntryCount    int
	UploadBytes   int64
	DownloadBytes int64
	InvalidCount  int
	UnlistedCount int
}

// NodeHourAdd 是节点小时汇总的一项增量（JSON 标签给 jsonb_to_recordset 用）。
type NodeHourAdd struct {
	NodeID   uuid.UUID `json:"node_id"`
	Upload   int64     `json:"upload"`
	Download int64     `json:"download"`
	Billed   int64     `json:"billed"`
}

// UserHourAdd 是节点 × 订阅小时汇总的一项增量。
type UserHourAdd struct {
	NodeID         uuid.UUID `json:"node_id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	Upload         int64     `json:"upload"`
	Download       int64     `json:"download"`
	Billed         int64     `json:"billed"`
}

// ServerNodeRates 取这台服务器上给定节点的流量倍率；不属于这台服务器的节点不在结果里（不看删除与在役状态，名单另判）。
func (r *Repo) ServerNodeRates(ctx context.Context, serverID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	out := make(map[uuid.UUID]decimal.Decimal, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ID          uuid.UUID       `gorm:"column:id"`
		TrafficRate decimal.Decimal `gorm:"column:traffic_rate"`
	}
	err := r.d.Conn(ctx).Raw(`SELECT id, traffic_rate FROM nodes WHERE server_id = ? AND id IN ?`, serverID, nodeIDs).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ID] = row.TrafficRate
	}
	return out, nil
}

// InsertTrafficReport 留档一份上报；同一 (服务器, 编号) 已有就什么都不做，返回 inserted=false。
// 同一编号的第一份还在别的事务里没提交时，这条语句等它结束再判冲突：并发的两份只有一份入账，对方回滚则本条插入成功。
func (r *Repo) InsertTrafficReport(ctx context.Context, row TrafficReportRow) (bool, error) {
	res := r.d.Conn(ctx).Exec(`
		INSERT INTO node_traffic_reports
		  (server_id, report_id, received_at, entries, content_sha256, entry_count,
		   upload_bytes, download_bytes, invalid_count, unlisted_count)
		VALUES (?, ?, ?, ?::jsonb, decode(?, 'hex'), ?, ?, ?, ?, ?)
		ON CONFLICT (server_id, report_id) DO NOTHING`,
		row.ServerID, row.ReportID, row.ReceivedAt, row.EntriesJSON, row.ContentSHA256, row.EntryCount,
		row.UploadBytes, row.DownloadBytes, row.InvalidCount, row.UnlistedCount)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// TrafficReportDigest 取已留档的那一份的内容摘要（小写 hex）。
func (r *Repo) TrafficReportDigest(ctx context.Context, serverID uuid.UUID, reportID string) (string, bool, error) {
	var rows []struct {
		Digest string `gorm:"column:digest"`
	}
	err := r.d.Conn(ctx).Raw(`SELECT encode(content_sha256, 'hex') AS digest
		FROM node_traffic_reports WHERE server_id = ? AND report_id = ?`, serverID, reportID).Scan(&rows).Error
	if err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Digest, true, nil
}

// satSumSQL 生成 bigint 的饱和加法表达式（超过上限封顶，不报溢出让整份上报回滚）。
func satSumSQL(cur, add string) string {
	return "CASE WHEN " + cur + " > 9223372036854775807 - " + add +
		" THEN 9223372036854775807 ELSE " + cur + " + " + add + " END"
}

// trafficHourlySQL 一条语句累加两张小时汇总：节点行按 node_id、用户行按 (node_id, subscription_id) 排序插入，
// 同一服务器的两份上报并发时加锁顺序一致。
var trafficHourlySQL = `
WITH n AS (
  INSERT INTO node_traffic_hourly AS h (node_id, hour_start, upload_bytes, download_bytes, billed_bytes)
  SELECT c.node_id, ?, c.upload, c.download, c.billed
    FROM jsonb_to_recordset(?::jsonb) AS c(node_id uuid, upload bigint, download bigint, billed bigint)
   ORDER BY c.node_id
  ON CONFLICT (node_id, hour_start) DO UPDATE SET
    upload_bytes   = ` + satSumSQL("h.upload_bytes", "EXCLUDED.upload_bytes") + `,
    download_bytes = ` + satSumSQL("h.download_bytes", "EXCLUDED.download_bytes") + `,
    billed_bytes   = ` + satSumSQL("h.billed_bytes", "EXCLUDED.billed_bytes") + `,
    updated_at     = now()
)
INSERT INTO node_user_traffic_hourly AS u (hour_start, node_id, subscription_id, upload_bytes, download_bytes, billed_bytes)
SELECT ?, c.node_id, c.subscription_id, c.upload, c.download, c.billed
  FROM jsonb_to_recordset(?::jsonb) AS c(node_id uuid, subscription_id uuid, upload bigint, download bigint, billed bigint)
 ORDER BY c.node_id, c.subscription_id
ON CONFLICT (hour_start, node_id, subscription_id) DO UPDATE SET
  upload_bytes   = ` + satSumSQL("u.upload_bytes", "EXCLUDED.upload_bytes") + `,
  download_bytes = ` + satSumSQL("u.download_bytes", "EXCLUDED.download_bytes") + `,
  billed_bytes   = ` + satSumSQL("u.billed_bytes", "EXCLUDED.billed_bytes") + `,
  updated_at     = now()`

// AddTrafficHourly 把一份上报累加进节点小时汇总与节点 × 订阅小时汇总（一次往返）。hour 是 UTC 整点。
func (r *Repo) AddTrafficHourly(ctx context.Context, hour time.Time, nodes []NodeHourAdd, users []UserHourAdd) error {
	if len(nodes) == 0 && len(users) == 0 {
		return nil
	}
	nj, err := jsonArray(nodes)
	if err != nil {
		return err
	}
	uj, err := jsonArray(users)
	if err != nil {
		return err
	}
	return r.d.Conn(ctx).Exec(trafficHourlySQL, hour, nj, hour, uj).Error
}

// jsonArray 编成 JSON 数组字符串（nil 切片编成 "[]"），给 jsonb_to_recordset 当一个参数用。
func jsonArray[T any](rows []T) (string, error) {
	if len(rows) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

// IsRetryable 判断是不是死锁（40P01）或序列化失败（40001）：整个事务已回滚，整笔重来是安全的。
func IsRetryable(err error) bool { return db.IsRetryable(err) }
