package upgrade

import (
	"context"
	"fmt"
)

// resume 在工作区已有 applying 记录时续做确认或回退。
func (h *Helper) resume(ctx context.Context, rec workRecord) error {
	state, err := openStateDir(h.stateParent())
	if err != nil {
		h.log().Warn("打开暂存目录失败，跳过对 pandora 目录的读写", "err", err)
		state = nil
	} else {
		defer state.Close()
	}

	cur, _ := sha256File(h.binPath())
	now := h.now().Unix()

	switch rec.Stage {
	case stageInstalling:
		if cur == rec.TargetSHA256 {
			rec.Stage = stageInstalled
			rec.StartedAt = now
			if err := h.saveRecord(rec); err != nil {
				h.log().Warn("保存续做记录失败", "job_id", rec.JobID, "err", err)
			}
			return h.confirm(ctx, state, rec)
		}
		if cur == rec.PrevSHA256 {
			h.finish(state, &rec, &Result{
				JobID:      rec.JobID,
				Phase:      PhaseFailed,
				ReasonCode: ReasonApplyFailed,
				Detail:     "助手被打断，现有安装没动",
			})
			return nil
		}
		return h.rollback(ctx, state, rec, PhaseFailed, ReasonApplyFailed, "助手被打断后二进制不明")

	case stageInstalled:
		if cur != rec.TargetSHA256 {
			return h.rollback(ctx, state, rec, PhaseFailed, ReasonApplyFailed, "二进制已被替换成别的版本")
		}
		if rec.Resumed {
			if h.healthyOK(state, rec) {
				return h.success(state, rec)
			}
			sec := int(ClampConfirm(rec.ConfirmWithinSec).Seconds())
			return h.rollback(ctx, state, rec, PhaseRolledBack, ReasonConfirmTimeout,
				fmt.Sprintf("新进程 %d 秒内没连上面板，已装回旧版", sec))
		}
		rec.Resumed = true
		rec.StartedAt = now
		if err := h.saveRecord(rec); err != nil {
			h.log().Warn("保存续做记录失败", "job_id", rec.JobID, "err", err)
		}
		return h.confirm(ctx, state, rec)

	case stageRollingBack:
		phase, code, detail := rec.ResultPhase, rec.ResultCode, rec.ResultDetail
		if phase == "" {
			phase = PhaseRolledBack
		}
		if code == "" {
			code = ReasonConfirmTimeout
		}
		if detail == "" {
			detail = "助手被打断后继续回退"
		}
		return h.rollback(ctx, state, rec, phase, code, detail)

	default:
		return h.rollback(ctx, state, rec, PhaseFailed, ReasonApplyFailed, "工作区记录不认识")
	}
}
