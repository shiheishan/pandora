package node

import (
	"bytes"
	"context"
	"slices"
	"strings"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/upgrade"
)

// Upgrader 是编排层用到的那一小块在线升级能力，*upgrade.Agent 实现它；测试用假的。
type Upgrader interface {
	Stage(ctx context.Context, req upgrade.StageRequest) error
	MarkHealthy() (bool, error)
	Result() (*upgrade.Result, error)
	RemoveResult(*upgrade.Result) error
}

// upgradeOutcome 是后台暂存的结局，经 Server.upgradeDone 回主循环。
type upgradeOutcome struct {
	jobID string
	err   error
}

func (s *Server) onWelcomeUpgrade(w *nodev1.Welcome, sent *nodev1.UpgradeReport) {
	s.upgradeNegotiated = s.upgrader != nil && slices.Contains(w.GetFeatures(), upgrade.Feature)
	if s.upgrader == nil {
		return
	}
	ok, err := s.upgrader.MarkHealthy()
	if err != nil {
		s.log.Warn("标记升级健康失败", "err", err)
	} else if ok {
		s.log.Info("新版本已连上面板，已告诉升级助手")
	}
	if sent != nil {
		r, err := s.upgrader.Result()
		if err != nil {
			s.log.Warn("读取升级结果失败", "err", err)
		} else if r != nil && r.JobID == sent.GetJobId() && phaseOf(r.Phase) == sent.GetPhase() && r.ReasonCode == sent.GetReasonCode() {
			if err := s.upgrader.RemoveResult(r); err != nil {
				s.log.Warn("删除已确认的升级结果失败", "err", err)
			}
		}
	}
	if s.pendingUpgrade != nil {
		s.flushPendingUpgrade()
	}
}

func (s *Server) onUpgrade(u *nodev1.Upgrade) {
	if s.upgrader == nil || !s.upgradeNegotiated {
		s.log.Warn("收到升级指令但本条会话没有协商 upgrade_v1，忽略", "job_id", u.GetJobId())
		return
	}
	if s.upgradeBusy {
		s.sendUpgradeReport(&nodev1.UpgradeReport{
			JobId:      u.GetJobId(),
			Phase:      nodev1.UpgradePhase_UPGRADE_PHASE_FAILED,
			ReasonCode: upgrade.ReasonBusy,
			Detail:     "已有一次升级在进行",
		})
		return
	}
	s.upgradeBusy = true
	req := upgrade.StageRequest{
		JobID:            u.GetJobId(),
		Manifest:         bytes.Clone(u.GetManifest()),
		Signature:        bytes.Clone(u.GetSignature()),
		BinaryURL:        u.GetBinaryUrl(),
		ConfirmWithinSec: u.GetConfirmWithinSec(),
	}
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	up := s.upgrader
	s.log.Info("开始暂存升级", "job_id", req.JobID)
	go func() {
		err := up.Stage(ctx, req)
		s.upgradeDone <- upgradeOutcome{jobID: req.JobID, err: err}
	}()
}

func (s *Server) onUpgradeDone(out upgradeOutcome) {
	s.upgradeBusy = false
	var rep *nodev1.UpgradeReport
	if out.err == nil {
		rep = &nodev1.UpgradeReport{
			JobId: out.jobID,
			Phase: nodev1.UpgradePhase_UPGRADE_PHASE_STAGED,
		}
		s.log.Info("升级暂存成功", "job_id", out.jobID)
	} else {
		code, detail := upgrade.ReasonOf(out.err, upgrade.ReasonDownloadFailed)
		rep = &nodev1.UpgradeReport{
			JobId:      out.jobID,
			Phase:      nodev1.UpgradePhase_UPGRADE_PHASE_FAILED,
			ReasonCode: code,
			Detail:     detail,
		}
		s.log.Warn("升级暂存失败", "job_id", out.jobID, "reason", code, "detail", detail)
	}
	s.sendUpgradeReport(rep)
}

func (s *Server) sendUpgradeReport(r *nodev1.UpgradeReport) {
	if r == nil {
		return
	}
	// proto 消息不能按值复制（内含锁）：新建一条
	clamped := &nodev1.UpgradeReport{
		JobId:      r.GetJobId(),
		Phase:      r.GetPhase(),
		ReasonCode: truncUTF8Bytes(r.GetReasonCode(), 64),
		Detail:     truncUTF8Bytes(r.GetDetail(), 1024),
	}
	if s.link != nil && s.link.SendUpgradeReport(clamped) {
		s.pendingUpgrade = nil
		return
	}
	s.pendingUpgrade = clamped
}

func (s *Server) flushPendingUpgrade() {
	if s.pendingUpgrade != nil && s.connected {
		s.sendUpgradeReport(s.pendingUpgrade)
	}
}

func (s *Server) upgradeHousekeeping() {
	if s.upgrader == nil || !s.connected {
		return
	}
	s.flushPendingUpgrade()
	r, err := s.upgrader.Result()
	if err != nil {
		s.log.Debug("内务读取升级结果失败", "err", err)
		return
	}
	if r == nil {
		return
	}
	rep := reportFromResult(r)
	if rep == nil {
		s.log.Warn("升级结果 phase 无法上报", "job_id", r.JobID, "phase", r.Phase)
		return
	}
	if s.link != nil && s.link.SendUpgradeReport(rep) {
		if err := s.upgrader.RemoveResult(r); err != nil {
			s.log.Warn("上报升级结局后删除结果失败", "err", err)
		}
		s.log.Info("已上报升级结局", "job_id", r.JobID, "phase", r.Phase, "reason", r.ReasonCode)
	}
}

// LastUpgrade 给 Hello 用（panel.SessionOptions.LastUpgrade）。会话在自己的 goroutine 里调：
// 只读升级结果文件、不碰 Server 的其他字段，因此并发安全。
func (s *Server) LastUpgrade() *nodev1.UpgradeReport {
	if s.upgrader == nil {
		return nil
	}
	r, err := s.upgrader.Result()
	if err != nil || r == nil {
		return nil
	}
	return reportFromResult(r)
}

func reportFromResult(r *upgrade.Result) *nodev1.UpgradeReport {
	ph := phaseOf(r.Phase)
	if ph == nodev1.UpgradePhase_UPGRADE_PHASE_UNSPECIFIED {
		return nil
	}
	return &nodev1.UpgradeReport{
		JobId:      r.JobID,
		Phase:      ph,
		ReasonCode: truncUTF8Bytes(r.ReasonCode, 64),
		Detail:     truncUTF8Bytes(r.Detail, 1024),
	}
}

func phaseOf(p string) nodev1.UpgradePhase {
	switch p {
	case upgrade.PhaseFailed:
		return nodev1.UpgradePhase_UPGRADE_PHASE_FAILED
	case upgrade.PhaseRolledBack:
		return nodev1.UpgradePhase_UPGRADE_PHASE_ROLLED_BACK
	default:
		return nodev1.UpgradePhase_UPGRADE_PHASE_UNSPECIFIED
	}
}

func truncUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxBytes], "")
}
