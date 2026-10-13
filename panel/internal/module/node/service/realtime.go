package service

// 节点与服务器的实时推送：后台的增删改与状态流转、身份变化、运行状态与配置回执的变化、在线状态翻转
// 推 nodes.changed，只推管理端（realtime.channelsFor 对 TopicNodes 的规则）。
// 不推：心跳时间、主机自报信息、指标、在线 IP、流量上报（高频，后台列表靠定时轮询刷新）。
// 在写成功的事务里调，提交后才真正发出；不在事务里调时立即发出（那条语句已自动提交）。

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 推送载荷里的表名。
const (
	rtTableNodes   = "nodes"
	rtTableServers = "servers"
)

// UseRealtime 接上实时推送（启动装配时调用，不并发安全）。没接上时不推。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// nodeChanged 推一条节点行变更。op 取 realtime.Op*。
func (s *Service) nodeChanged(ctx context.Context, op string, id uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicNodes, rtTableNodes, op, id, nil)
}

// nodesChanged 对 ids 里每个节点各推一条 UPDATE。
func (s *Service) nodesChanged(ctx context.Context, ids []uuid.UUID) {
	for _, id := range ids {
		s.nodeChanged(ctx, realtime.OpUpdate, id)
	}
}

// serverChanged 推一条服务器行变更。op 取 realtime.Op*。
func (s *Service) serverChanged(ctx context.Context, op string, id uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicNodes, rtTableServers, op, id, nil)
}
