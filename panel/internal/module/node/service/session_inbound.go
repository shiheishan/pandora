package service

// 会话收到的消息（传输层的接收循环逐条调；同一条流里不并发）。落库、入账失败只记日志，不断流：
// 节点会重发流量（没有 ack），心跳下一拍再写，回执与运行状态下次变化再写。

import (
	"context"
	"errors"
	"math/big"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// log 是这条会话的 logger：server_id 已由 gRPC 鉴权拦截器挂在 ctx 的 logger 上，这里只加 session_id。
func (s *Session) log(ctx context.Context) *zap.Logger {
	return plog.From(ctx).With(zap.String("session_id", s.id))
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

// HandleTraffic 入账一份流量上报。成功、同 id 内容不同（ErrTrafficReportConflict）或上报不合规（ErrTrafficReportInvalid）
// 都回 TrafficAck；后两种是节点端缺陷，不回 ack 节点会原样重发、死循环。其它错误不回 ack（节点保留这一批，重连后按原 id 重发）。
func (s *Session) HandleTraffic(ctx context.Context, r TrafficReport) {
	err := s.m.b.IngestTraffic(ctx, s.serverID, r, s)
	switch {
	case err == nil:
	case errors.Is(err, ErrTrafficReportConflict), errors.Is(err, ErrTrafficReportInvalid):
		s.log(ctx).Error("上报与已入账的同编号内容不同或上报不合规：不入账，照样回 ack", zap.String("report_id", r.ReportID), zap.Error(err))
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

// HandleRenew 用建流时出示的证书序列号与 CSR 续期，成功回 Certificate。certSerial 是建流时出示的证书序列号，
// 续期只在它仍是这台服务器 active 那张时受理；证书剩余有效期还长或刚续过时面板拒绝（记 Warn，不断流）。
// 身份已失效时对本会话 Tombstone；其它错误只记日志（节点稍后再试）。
func (s *Session) HandleRenew(ctx context.Context, certSerial *big.Int, csrDER []byte) {
	chain, ca, err := s.m.b.RenewCertificate(ctx, s.serverID, certSerial, csrDER)
	if err != nil {
		if errors.Is(err, ErrSessionRevoked) {
			s.log(ctx).Warn("续期时发现身份已失效，发 Tombstone")
			s.Tombstone(TombstoneIdentityRevoked)
			return
		}
		if errors.Is(err, errRenewTooEarly) || errors.Is(err, errRenewTooFrequent) {
			// 剩余有效期还长、刚续过：面板按规则拒绝，节点稍后再试，不是故障
			s.log(ctx).Warn("续期暂不受理", zap.Error(err))
			return
		}
		s.log(ctx).Error("续期失败", zap.Error(err))
		return
	}
	s.enqueue(Outbound{Kind: OutCertificate, CertChainPEM: chain, CAPEM: ca})
}
