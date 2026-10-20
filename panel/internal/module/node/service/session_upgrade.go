package service

// 在线升级在会话层的部分：升级事实只取自建流时的 Hello，不另存；Command.upgrade 作为普通消息排队（计入队列上限）；
// UpgradeReport 交数据面对账（upgrades.go）。

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// UpgradeInfo 返回这台服务器在途会话的升级事实；没有在途会话或会话在结束中时 ok 为 false。
func (m *Sessions) UpgradeInfo(serverID uuid.UUID) (SessionUpgradeInfo, bool) {
	s := m.get(serverID)
	if s == nil || s.Ending() {
		return SessionUpgradeInfo{}, false
	}
	return s.upgrade, true
}

// SendUpgrade 给这台服务器的在途会话排一条 Command.upgrade：没有在途会话、没协商 upgrade_v1、会话在结束中或队列满时返回 false。
func (m *Sessions) SendUpgrade(serverID uuid.UUID, cmd UpgradeCommand) bool {
	s := m.get(serverID)
	if s == nil || !s.upgrade.UpgradeV1 {
		return false
	}
	return s.enqueue(Outbound{Kind: OutUpgrade, Upgrade: cmd})
}

// HandleUpgradeReport 把节点的升级回报交数据面对账；失败只记日志，不断流（节点下次 Hello 还会带上结局）。
func (s *Session) HandleUpgradeReport(ctx context.Context, r UpgradeReport) {
	if err := s.m.b.RecordUpgradeReport(ctx, s.serverID, r, s.m.now()); err != nil {
		s.log(ctx).Error("记录升级回报失败", zap.String("job_id", r.JobID.String()), zap.Int("phase", int(r.Phase)), zap.Error(err))
	}
}
