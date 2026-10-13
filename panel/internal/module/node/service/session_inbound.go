package service

// 会话收到的消息（传输层的接收循环逐条调；同一条流里不并发）。落库、入账失败只记日志，不断流：
// 节点会重发流量（没有 ack），心跳下一拍再写，回执与运行状态下次变化再写。

import (
	"context"
	"errors"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

func (s *Session) log(ctx context.Context) *zap.Logger {
	return plog.From(ctx).With(zap.String("server_id", s.serverID.String()), zap.String("session_id", s.id))
}

// HandleHeartbeat 记一拍心跳：服务器的最后心跳时间（每拍写）、各节点运行状态（只在变化时写）、机器指标。
func (s *Session) HandleHeartbeat(ctx context.Context, hb Heartbeat) {
	at := s.m.now()
	if err := s.m.b.RecordHeartbeat(ctx, s.serverID, hb.Nodes, at); err != nil {
		s.log(ctx).Error("记录心跳失败", zap.Error(err))
	}
	if hb.Metrics != nil {
		if err := s.m.b.RecordMetrics(ctx, s.serverID, *hb.Metrics, at); err != nil {
			s.log(ctx).Warn("记录机器指标失败", zap.Error(err))
		}
	}
}

// HandleConfigAck 记配置回执。摘要不是这个节点最近下发（或 Hello 时已一致）的那份，记 WARN 忽略。
func (s *Session) HandleConfigAck(ctx context.Context, a ConfigAck) {
	if !s.configAckExpected(a.NodeID, a.SHA256) {
		s.log(ctx).Warn("配置回执的摘要不是最近下发的，忽略",
			zap.String("node_id", a.NodeID.String()), zap.String("sha256", a.SHA256), zap.Int("phase", int(a.Phase)))
		return
	}
	if err := s.m.b.RecordConfigAck(ctx, s.serverID, a, s.m.now()); err != nil {
		s.log(ctx).Error("记录配置回执失败", zap.String("node_id", a.NodeID.String()), zap.Error(err))
	}
}

// HandleTraffic 入账一份流量上报。成功或同 id 内容不同（ErrTrafficReportConflict，节点端 bug，记 ERROR 不入账）
// 都回 TrafficAck；其它错误不回 ack（节点保留这一批，重连后按原 id 重发）。
func (s *Session) HandleTraffic(ctx context.Context, r TrafficReport) {
	err := s.m.b.IngestTraffic(ctx, s.serverID, r, s)
	switch {
	case err == nil:
	case errors.Is(err, ErrTrafficReportConflict):
		s.log(ctx).Error("同一上报编号内容不同，不入账，照样回 ack", zap.String("report_id", r.ReportID))
	default:
		s.log(ctx).Error("流量入账失败，不回 ack，等节点重发", zap.String("report_id", r.ReportID), zap.Error(err))
		return
	}
	s.enqueue(Outbound{Kind: OutTrafficAck, ReportID: r.ReportID})
}

// AckTraffic 只回 TrafficAck 不入账：传输层判定上报本身不合法（节点端 bug）时用，免得节点无限重发。
func (s *Session) AckTraffic(reportID string) {
	s.enqueue(Outbound{Kind: OutTrafficAck, ReportID: reportID})
}

// HandleOnline 记一批在线 IP（名单外的 uid 由入账方丢弃）。
func (s *Session) HandleOnline(ctx context.Context, entries []OnlineEntry) {
	if err := s.m.b.IngestOnline(ctx, s.serverID, entries, s); err != nil {
		s.log(ctx).Error("记录在线 IP 失败", zap.Error(err))
	}
}

// HandleRenew 用会话里的 CSR 续期，成功回 Certificate。身份已失效时发 Tombstone 关流；其它错误只记日志（节点稍后再试）。
func (s *Session) HandleRenew(ctx context.Context, csrDER []byte) {
	chain, ca, err := s.m.b.RenewCertificate(ctx, s.serverID, csrDER)
	if err != nil {
		if errors.Is(err, ErrSessionRevoked) {
			s.log(ctx).Warn("续期时发现身份已失效，发 Tombstone")
			s.terminate(Outbound{Kind: OutTombstone, Reason: "identity revoked"}, ErrSessionTombstoned)
			return
		}
		s.log(ctx).Error("续期失败", zap.Error(err))
		return
	}
	s.enqueue(Outbound{Kind: OutCertificate, CertChainPEM: chain, CAPEM: ca})
}
