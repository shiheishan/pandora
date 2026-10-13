package service

// 流量入账、在线 IP、机器指标（p5-traffic 实现）。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrTrafficReportConflict 是同一服务器同一 report_id 再来一份内容不同的上报。会话层回给节点错误、不发 TrafficAck。
var ErrTrafficReportConflict = errors.New("node: 同一上报编号内容不同")

// IngestTraffic 入账一份流量上报：留档、按节点倍率折算后经 subscription 扣量、写汇总，同一事务。
// 同一 (服务器, report_id) 重复到达且内容相同：不再扣量，返回 nil（会话层照样回 TrafficAck）。
// m 判定名单成员：名单外的条目留档不扣费。
func (s *Service) IngestTraffic(ctx context.Context, serverID uuid.UUID, r TrafficReport, m RosterMembership) error {
	panic("p5-traffic: IngestTraffic")
}

// IngestOnline 记一批在线 IP（名单外的 uid 丢弃）。
func (s *Service) IngestOnline(ctx context.Context, serverID uuid.UUID, entries []OnlineEntry, m RosterMembership) error {
	panic("p5-traffic: IngestOnline")
}

// RecordMetrics 记一个机器指标点。
func (s *Service) RecordMetrics(ctx context.Context, serverID uuid.UUID, m HostMetrics, at time.Time) error {
	panic("p5-traffic: RecordMetrics")
}
