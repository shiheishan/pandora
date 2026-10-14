package service

// 流量上报入账。在线 IP 在 online.go，机器指标在 metrics.go。

import (
	"context"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// ErrTrafficReportConflict 是同一服务器同一 report_id 再来一份内容不同的上报。只有节点端缺陷才会出现：
// 入账方已记 ERROR 日志（带 req_id、server_id、report_id 与两份摘要），会话层照样回 TrafficAck（节点丢掉这份）、不入账，
// 否则节点重连后原样重发、死循环。
var ErrTrafficReportConflict = errors.New("node: 同一上报编号内容不同")

// errChargerMissing 是没装配订阅扣量（启动装配漏了）。
var errChargerMissing = errors.New("node: 未装配订阅扣量")

// trafficRetryAttempts 是入账事务遇到死锁或序列化失败时的总尝试次数（整笔已回滚，重来是安全的）。
const trafficRetryAttempts = 3

// IngestTraffic 入账一份流量上报：留档、按节点倍率折算后经 subscription 扣量、写小时汇总，同一事务。
//
// 顺序：读节点倍率 → 留档（UNIQUE(server_id, report_id) 下 ON CONFLICT DO NOTHING）→ 扣量（subscription：
// 配额行 → 流量包 → 按日用量）→ 两张小时汇总（一条语句）。回滚则都不留，提交则都在。
// 同一 (服务器, report_id) 重复到达且内容摘要相同：什么都不做，返回 nil（会话层照样回 TrafficAck）；内容不同返回 ErrTrafficReportConflict。
// 倍率是节点当前的 traffic_rate，decimal 向零截断（每个节点上每个用户一次）。名单外（m）与不合规的条目照样留档、计入留档的计数，不扣费。
// 遇 40P01 / 40001 整笔重试，最多 trafficRetryAttempts 次；所以不要在已有事务里调（已中止的事务里重试只会报 25P02）。
func (s *Service) IngestTraffic(ctx context.Context, serverID uuid.UUID, r TrafficReport, m RosterMembership) error {
	if !validReportID(r.ReportID) || len(r.Entries) > maxTrafficReportEntries {
		return ErrTrafficReportInvalid
	}
	if s.charger == nil {
		return errChargerMissing
	}
	sorted, digest := canonicalTraffic(r.Entries)
	entriesJSON, err := archiveJSON(sorted)
	if err != nil {
		return err
	}
	in := trafficIngest{serverID: serverID, reportID: r.ReportID, entries: sorted, entriesJSON: entriesJSON, digest: hex.EncodeToString(digest[:])}
	for attempt := 1; ; attempt++ {
		err := s.tx.Do(ctx, func(ctx context.Context) error { return s.ingestTrafficTx(ctx, in, m) })
		if err == nil || attempt >= trafficRetryAttempts || !repo.IsRetryable(err) || ctx.Err() != nil {
			return err
		}
		// 死锁的另一方（多半是续费、重置或配额滚动）刚回滚或提交，稍等再整笔重来
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt*attempt) * 5 * time.Millisecond):
		}
	}
}

// trafficIngest 是一份已校验、排好序的上报。
type trafficIngest struct {
	serverID    uuid.UUID
	reportID    string
	entries     []TrafficEntry
	entriesJSON string
	digest      string
}

// ingestTrafficTx 在事务里入账一份上报（见 IngestTraffic）。
func (s *Service) ingestTrafficTx(ctx context.Context, in trafficIngest, m RosterMembership) error {
	now := s.now().UTC().Truncate(time.Microsecond)
	nodeIDs := make([]uuid.UUID, 0, 4)
	seen := map[uuid.UUID]bool{}
	for _, e := range in.entries {
		if !seen[e.NodeID] {
			seen[e.NodeID] = true
			nodeIDs = append(nodeIDs, e.NodeID)
		}
	}
	rates, err := s.repo.ServerNodeRates(ctx, in.serverID, nodeIDs)
	if err != nil {
		return err
	}
	t := tallyTraffic(in.entries, rates, m)

	inserted, err := s.repo.InsertTrafficReport(ctx, repo.TrafficReportRow{
		ServerID: in.serverID, ReportID: in.reportID, ReceivedAt: now,
		EntriesJSON: in.entriesJSON, ContentSHA256: in.digest, EntryCount: len(in.entries),
		UploadBytes: t.Upload, DownloadBytes: t.Download, InvalidCount: t.Invalid, UnlistedCount: t.Unlisted,
	})
	if err != nil {
		return err
	}
	if !inserted {
		existing, found, err := s.repo.TrafficReportDigest(ctx, in.serverID, in.reportID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("node: 流量上报编号冲突却查不到原件")
		}
		if existing == in.digest {
			return nil // 重放：已入账过，不再扣
		}
		// server_id 已由 gRPC 鉴权拦截器挂在流 ctx 的 logger 上
		plog.From(ctx).Error("同一上报编号内容不同，不入账", zap.String("report_id", in.reportID),
			zap.String("existing_sha256", existing), zap.String("new_sha256", in.digest))
		return ErrTrafficReportConflict
	}

	subs, err := s.charger.ChargeTraffic(ctx, t.charges())
	if err != nil {
		return err
	}
	nodes, users := t.hourlyRows(subs)
	return s.repo.AddTrafficHourly(ctx, now.Truncate(time.Hour), nodes, users)
}
