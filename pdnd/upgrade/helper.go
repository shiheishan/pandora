package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// workRecord 是 WorkDir/applying：助手进行中的那一次升级，被打断后据此续做。只在 root 的 0700 工作区里，可信。
type workRecord struct {
	JobID            string `json:"job_id"`
	TargetSHA256     string `json:"target_sha256"`
	PrevSHA256       string `json:"prev_sha256"`
	ConfirmWithinSec uint32 `json:"confirm_within_sec"`
	StartedAt        int64  `json:"started_at"` // Unix 秒；确认时限从这里算
	Stage            string `json:"stage"`      // stageInstalling / stageInstalled / stageRollingBack
	Resumed          bool   `json:"resumed"`    // 已经续做过一次确认：再被打断就不再给新的确认窗口
	// 进入回退时记下要写的结局，续做回退时照用。
	ResultPhase  string `json:"result_phase,omitempty"`
	ResultCode   string `json:"result_code,omitempty"`
	ResultDetail string `json:"result_detail,omitempty"`
}

const (
	stageInstalling  = "installing"
	stageInstalled   = "installed"
	stageRollingBack = "rolling_back"
)

// Helper 是 root 侧升级助手：从 pandora 暂存目录取件、验签、替换二进制并确认或回退。
type Helper struct {
	StateParent  string // DefaultStateParent；StateDir = StateParent/StateSubdir
	WorkDir      string // DefaultWorkDir
	BinPath      string // DefaultBinPath；.prev / .new 在同目录
	Keyring      *Keyring
	BuildSerial  uint64
	GOARCH       string
	Service      ServiceManager
	Unit         string                                           // 空即 MainUnit
	Now          func() time.Time                                 // nil 即 time.Now
	Sleep        func(ctx context.Context, d time.Duration) error // nil 即真睡
	PollInterval time.Duration                                    // 0 即 1 秒
	Log          *slog.Logger
}

// Run 执行一次升级助手主流程：处理新 request 或续做工作区里的 applying。
func (h *Helper) Run(ctx context.Context) error {
	if err := ensureWorkDir(h.workDir()); err != nil {
		return err
	}

	applyingPath := h.workApplyingPath()
	if _, err := os.Lstat(applyingPath); err == nil {
		rec, err := h.loadRecord()
		if err != nil {
			h.log().Error("工作区 applying 解析失败，清空工作区", "err", err)
			h.removeWorkArtifacts()
			return nil
		}
		return h.resume(ctx, rec)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	state, err := openStateDir(h.stateParent())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer state.Close()

	if _, err := state.Lstat(FileRequest); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	return h.take(ctx, state)
}

func (h *Helper) stateParent() string {
	if h.StateParent != "" {
		return h.StateParent
	}
	return DefaultStateParent
}

func (h *Helper) workDir() string {
	if h.WorkDir != "" {
		return h.WorkDir
	}
	return DefaultWorkDir
}

func (h *Helper) binPath() string {
	if h.BinPath != "" {
		return h.BinPath
	}
	return DefaultBinPath
}

func (h *Helper) unit() string {
	if h.Unit != "" {
		return h.Unit
	}
	return MainUnit
}

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Helper) sleep() func(context.Context, time.Duration) error {
	if h.Sleep != nil {
		return h.Sleep
	}
	return func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
}

func (h *Helper) poll() time.Duration {
	if h.PollInterval > 0 {
		return h.PollInterval
	}
	return time.Second
}

func (h *Helper) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Helper) prevPath() string { return h.binPath() + ".prev" }
func (h *Helper) newPath() string  { return h.binPath() + ".new" }

func (h *Helper) workApplyingPath() string {
	return filepath.Join(h.workDir(), FileApplying)
}

func (h *Helper) workFile(name string) string {
	return filepath.Join(h.workDir(), name)
}

func (h *Helper) saveRecord(rec workRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return writeFileSync(h.workApplyingPath(), data, 0o600)
}

func (h *Helper) loadRecord() (workRecord, error) {
	data, err := os.ReadFile(h.workApplyingPath())
	if err != nil {
		return workRecord{}, err
	}
	var rec workRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return workRecord{}, err
	}
	return rec, nil
}

func (h *Helper) removeWorkArtifacts() {
	entries, err := os.ReadDir(h.workDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(h.workDir(), e.Name()))
	}
}

func (h *Helper) take(ctx context.Context, state *os.Root) error {
	wd := h.workDir()
	copyErrs := make(map[string]error)
	for _, name := range []string{FileRequest, FileNext, FileManifest, FileSignature} {
		dst := filepath.Join(wd, name)
		var max int64
		switch name {
		case FileRequest:
			max = MaxSmallFileBytes
		case FileNext:
			max = MaxBinaryBytes
		case FileManifest:
			max = MaxManifestBytes
		case FileSignature:
			max = MaxSignatureBytes
		}
		if err := copyFromState(state, name, dst, max); err != nil {
			copyErrs[name] = err
		}
	}
	for _, name := range []string{FileNext, FileManifest, FileSignature} {
		_ = removeState(state, name)
	}
	// request 删不掉（被换成非空目录等）时 path 单元会立即再触发：报错退出，让 journal 与单元状态看得出来
	if err := removeState(state, FileRequest); err != nil {
		h.log().Error("删除暂存 request 失败", "err", err)
		h.removeWorkArtifacts()
		return fmt.Errorf("删除暂存 request: %w", err)
	}

	reqData, err := os.ReadFile(h.workFile(FileRequest))
	if err != nil || copyErrs[FileRequest] != nil {
		if copyErrs[FileRequest] != nil {
			h.log().Warn("request 暂存不可用", "err", copyErrs[FileRequest])
		} else {
			h.log().Warn("request 读取失败", "err", err)
		}
		h.removeWorkArtifacts()
		return nil
	}
	var req Request
	if err := json.Unmarshal(reqData, &req); err != nil || !ValidJobID(req.JobID) {
		h.log().Warn("request JSON 无效或 job_id 不合规")
		h.removeWorkArtifacts()
		return nil
	}

	for _, name := range []string{FileNext, FileManifest, FileSignature} {
		if copyErrs[name] != nil {
			h.finish(state, nil, &Result{
				JobID:      req.JobID,
				Phase:      PhaseFailed,
				ReasonCode: ReasonApplyFailed,
				Detail:     fmt.Sprintf("暂存文件不可用：%v", copyErrs[name]),
			})
			return nil
		}
	}

	manifest, err := os.ReadFile(h.workFile(FileManifest))
	if err != nil {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: "暂存文件不可用：" + err.Error()})
		return nil
	}
	sig, err := os.ReadFile(h.workFile(FileSignature))
	if err != nil {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: "暂存文件不可用：" + err.Error()})
		return nil
	}

	_, art, err := Check(h.Keyring, manifest, sig, h.BuildSerial, h.GOARCH)
	if err != nil {
		code, detail := ReasonOf(err, ReasonManifestInvalid)
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: code, Detail: detail})
		return nil
	}

	nextSum, err := sha256File(h.workFile(FileNext))
	if err != nil {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: "暂存文件不可用：" + err.Error()})
		return nil
	}
	if nextSum != art.SHA256 {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonSHA256Mismatch, Detail: "二进制摘要与清单不符"})
		return nil
	}

	return h.install(ctx, state, req, art)
}

func (h *Helper) install(ctx context.Context, state *os.Root, req Request, art Artifact) error {
	prev, err := sha256File(h.binPath())
	if err != nil {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: "读不到现有二进制"})
		return nil
	}
	if err := copyFileSync(h.binPath(), h.prevPath(), 0o755); err != nil {
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: err.Error()})
		return nil
	}
	if err := copyFileSync(h.workFile(FileNext), h.newPath(), 0o755); err != nil {
		_ = os.Remove(h.newPath())
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: err.Error()})
		return nil
	}

	rec := workRecord{
		JobID:            req.JobID,
		TargetSHA256:     art.SHA256,
		PrevSHA256:       prev,
		ConfirmWithinSec: req.ConfirmWithinSec,
		StartedAt:        h.now().Unix(),
		Stage:            stageInstalling,
	}
	if err := h.saveRecord(rec); err != nil {
		_ = os.Remove(h.newPath())
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: err.Error()})
		return nil
	}
	if err := renameSync(h.newPath(), h.binPath()); err != nil {
		_ = os.Remove(h.newPath())
		h.finish(state, nil, &Result{JobID: req.JobID, Phase: PhaseFailed, ReasonCode: ReasonApplyFailed, Detail: err.Error()})
		return nil
	}

	rec.Stage = stageInstalled
	rec.StartedAt = h.now().Unix()
	if err := h.saveRecord(rec); err != nil {
		h.log().Warn("更新工作区 applying 失败", "job_id", req.JobID, "err", err)
	}
	return h.confirm(ctx, state, rec, false)
}

// confirm 写好确认标记后重启并等 healthy。running 表示新版可能已在运行（续做时），
// 写不了标记要回退时据此决定装回旧版后要不要重启。
func (h *Helper) confirm(ctx context.Context, state *os.Root, rec workRecord, running bool) error {
	// 新进程只认暂存 applying 才会写 healthy：写不了就必然超时回退，而面板已在新 Hello 上记成功。所以不重启，直接装回旧版。
	if err := h.prepareMarker(state, rec); err != nil {
		return h.rollbackWith(ctx, state, rec, PhaseFailed, ReasonApplyFailed, fmt.Sprintf("写不了确认标记：%v", err), running)
	}

	if err := h.Service.Restart(ctx, h.unit()); err != nil {
		return h.rollback(ctx, state, rec, PhaseFailed, ReasonApplyFailed, fmt.Sprintf("重启失败：%v", err))
	}

	deadline := time.Unix(rec.StartedAt, 0).Add(ClampConfirm(rec.ConfirmWithinSec))
	for {
		if h.healthyOK(state, rec) {
			return h.success(state, rec)
		}
		if !h.now().Before(deadline) {
			break
		}
		if err := h.sleep()(ctx, h.poll()); err != nil {
			return err
		}
	}
	sec := int(ClampConfirm(rec.ConfirmWithinSec).Seconds())
	return h.rollback(ctx, state, rec, PhaseRolledBack, ReasonConfirmTimeout,
		fmt.Sprintf("新进程 %d 秒内没连上面板，已装回旧版", sec))
}

// prepareMarker 写暂存 applying 并删掉旧的 healthy；暂存目录打不开时同样算失败。
func (h *Helper) prepareMarker(state *os.Root, rec workRecord) error {
	if state == nil {
		return errors.New("暂存目录不可用")
	}
	marker, err := json.Marshal(Marker{JobID: rec.JobID, SHA256: rec.TargetSHA256})
	if err != nil {
		return err
	}
	if err := writeState(state, FileApplying, marker); err != nil {
		return err
	}
	return removeState(state, FileHealthy)
}

func (h *Helper) healthyOK(state *os.Root, rec workRecord) bool {
	if state == nil {
		return false
	}
	raw, err := readState(state, FileHealthy, MaxSmallFileBytes)
	if err != nil {
		return false
	}
	var m Marker
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return m.JobID == rec.JobID && m.SHA256 == rec.TargetSHA256
}

func (h *Helper) success(state *os.Root, rec workRecord) error {
	h.log().Info("升级成功", "job_id", rec.JobID)
	h.finish(state, &rec, nil)
	return nil
}

func (h *Helper) rollback(ctx context.Context, state *os.Root, rec workRecord, phase, code, detail string) error {
	return h.rollbackWith(ctx, state, rec, phase, code, detail, true)
}

// rollbackWith：restart 为 false 时只装回 .prev、不重启（新版还没被重启拉起，跑着的仍是旧进程）。
func (h *Helper) rollbackWith(ctx context.Context, state *os.Root, rec workRecord, phase, code, detail string, restart bool) error {
	rec.Stage = stageRollingBack
	rec.ResultPhase = phase
	rec.ResultCode = code
	rec.ResultDetail = detail
	if err := h.saveRecord(rec); err != nil {
		h.log().Warn("保存回退中的工作区记录失败", "job_id", rec.JobID, "err", err)
	}

	cur, _ := sha256File(h.binPath())
	if cur != rec.PrevSHA256 {
		prevSum, err := sha256File(h.prevPath())
		if err != nil || prevSum != rec.PrevSHA256 {
			var d string
			if err != nil {
				d = fmt.Sprintf(".prev 不可用：%v（原因：%s）", err, detail)
			} else {
				d = fmt.Sprintf(".prev 不可用：摘要不匹配（原因：%s）", detail)
			}
			h.finish(state, &rec, &Result{JobID: rec.JobID, Phase: PhaseFailed, ReasonCode: ReasonRollbackFailed, Detail: d})
			return nil
		}
		if err := copyFileSync(h.prevPath(), h.newPath(), 0o755); err != nil {
			h.finish(state, &rec, &Result{JobID: rec.JobID, Phase: PhaseFailed, ReasonCode: ReasonRollbackFailed, Detail: err.Error()})
			return nil
		}
		if err := renameSync(h.newPath(), h.binPath()); err != nil {
			h.finish(state, &rec, &Result{JobID: rec.JobID, Phase: PhaseFailed, ReasonCode: ReasonRollbackFailed, Detail: err.Error()})
			return nil
		}
	}

	if !restart {
		h.finish(state, &rec, &Result{JobID: rec.JobID, Phase: phase, ReasonCode: code, Detail: detail})
		return nil
	}
	if err := h.Service.Restart(ctx, h.unit()); err != nil {
		h.finish(state, &rec, &Result{
			JobID:      rec.JobID,
			Phase:      PhaseFailed,
			ReasonCode: ReasonRollbackFailed,
			Detail:     fmt.Sprintf("装回旧版后重启失败：%v", err),
		})
		return nil
	}

	h.finish(state, &rec, &Result{JobID: rec.JobID, Phase: phase, ReasonCode: code, Detail: detail})
	return nil
}

func (h *Helper) finish(state *os.Root, rec *workRecord, res *Result) {
	if res != nil && state != nil {
		data, err := json.Marshal(res)
		if err != nil {
			h.log().Error("序列化结果失败", "job_id", res.JobID, "err", err)
		} else if err := writeState(state, FileResult, data); err != nil {
			h.log().Error("写入结果文件失败", "job_id", res.JobID, "err", err)
		}
		h.log().Warn("升级未成功结束", "job_id", res.JobID, "phase", res.Phase, "reason", res.ReasonCode)
	}
	if state != nil {
		_ = removeState(state, FileApplying)
		_ = removeState(state, FileHealthy)
	}
	for _, name := range []string{FileRequest, FileNext, FileManifest, FileSignature} {
		_ = os.Remove(h.workFile(name))
	}
	_ = os.Remove(h.workApplyingPath())
	// 提前结束的路径可能留下半截的 .new / .tmp；.prev 留着给下次回退
	for _, p := range []string{h.newPath(), h.newPath() + ".tmp", h.prevPath() + ".tmp"} {
		_ = os.Remove(p)
	}
}
