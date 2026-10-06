package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

const effectiveHealthStabilityWindow = 5 * time.Second

// maxReportDetailBytes 是面板对配置上报 detail 的上限（超出整条 422 拒收）。
const maxReportDetailBytes = 2048

// ---------------------------------------------------------------------------
// 版本身份与失败台账
// ---------------------------------------------------------------------------

// signedConfigKey 是签名配置的版本身份，只由「内核会装成什么样」决定。
//
// 面板每次投递都重新签发（生效发布的投递窗口最长 10 分钟）：issued_at、
// expires_at、签名每轮都变，内容不变。拿这些签发元数据认版本，同一份坏配置
// 每轮都像新的。反过来，release_id + generation 相同而内容哈希不同，内核装
// 的就是另一份东西，必须当新版本再试——哈希已由 VerifyConfig 对 payload 核过。
type signedConfigKey struct {
	releaseID  string
	generation uint64
	version    int
	content    string
}

func signedConfigKeyOf(cfg *panel.SignedConfig) signedConfigKey {
	if cfg.ConfigContract != "" {
		return signedConfigKey{releaseID: cfg.ReleaseID, generation: cfg.Generation, content: cfg.ContentSHA256}
	}
	return signedConfigKey{version: cfg.Version, content: cfg.Hash}
}

// signedApplyFailure 记下最近一个装不上的签名配置版本。
//
// 只占一个槽：面板发了新版本就覆盖，任何版本装上就清掉。
type signedApplyFailure struct {
	key signedConfigKey
	// detail 是首次失败的原因，补报时原样重发：生效回执按 report_id 去重，
	// 同一 report_id 带不同 detail 会被面板当成冲突的证据拒收。
	detail string
	// settled 为真表示面板已收下这条 failed，或已明确拒收（4xx），不再上报。
	settled bool
}

// syncSignedConfig 是签名通道的一轮配置同步。
//
// 同一个装不上的版本分两种处境：
//   - 旧配置仍在服务（整版回滚成功，或内核 PreviousPreserved）：不再试装。
//     整版回滚的内核每试一次就是两次入站重建，全部在线连接断两次；代际内核
//     也是白做一轮预检。返回 nil，让 syncOnce 照常同步用户——坏版本挂着期间
//     用户变更不能跟着停。
//   - 节点已停（首个配置就没装上、回滚也失败、或进程刚重启）：每轮都重试。
//     没有旧配置可保，重试没有代价；端口暂被占用这类故障一消失就该恢复。
//     节拍就是拉取间隔，不另加退避，与兼容通道作废配置 ETag 的语义一致。
//
// 失败对每个版本只报一次（送不到就随后的拉取补报，面板明确拒收就作罢）；
// 面板另从心跳的 applied_effective_* 看出节点仍停在旧版本。
func (n *Node) syncSignedConfig(ctx context.Context) error {
	cfg, err := n.signed.Config(ctx)
	if err != nil {
		return err
	}
	if err := n.signed.VerifyConfig(cfg); err != nil {
		n.reportSignedConfigPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if n.started && n.signedConfigAlreadyApplied(cfg) {
		// Reports use a deterministic id per release and phase, so replaying
		// both phases safely repairs a response lost after the local switch.
		if err := n.reportSignedConfigPhase(ctx, cfg, "switched", ""); err != nil {
			return fmt.Errorf("retry effective config switched report: %w", err)
		}
		if !n.effectiveHealthReady(time.Now()) {
			return nil
		}
		if err := n.reportSignedConfigPhase(ctx, cfg, "health_passed", ""); err != nil {
			return fmt.Errorf("retry effective config health report: %w", err)
		}
		return nil
	}
	key := signedConfigKeyOf(cfg)
	if failed := n.failedSigned; n.started && failed != nil && failed.key == key {
		n.reportSignedFailure(ctx, cfg)
		return nil
	}
	if err := n.applySignedConfig(cfg); err != nil {
		if n.failedSigned == nil || n.failedSigned.key != key {
			n.failedSigned = &signedApplyFailure{key: key, detail: err.Error()}
		}
		n.reportSignedFailure(ctx, cfg)
		return err
	}
	n.failedSigned = nil
	n.recordAppliedSignedConfig(cfg)
	if err := n.reportSignedConfigPhase(ctx, cfg, "switched", ""); err != nil {
		n.log.Warn("配置已应用但切换上报失败", "release_id", cfg.ReleaseID, "generation", cfg.Generation, "err", err)
	}
	return nil
}

// applySignedConfig 解出已验签的 payload 交给 applyConfig。解析失败与应用失败
// 一样是这一版本身的问题，同一内容再解一次结果不会变。
func (n *Node) applySignedConfig(cfg *panel.SignedConfig) error {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(cfg.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	return n.applyConfig(raw)
}

// reportSignedFailure 把 failedSigned 报给面板，直到面板收下或明确拒收。
func (n *Node) reportSignedFailure(ctx context.Context, cfg *panel.SignedConfig) {
	failed := n.failedSigned
	if failed == nil || failed.settled {
		return
	}
	err := n.reportSignedConfigPhase(ctx, cfg, "failed", failed.detail)
	failed.settled = reportSettled(err)
	if err != nil {
		n.log.Warn("配置应用失败的上报未被面板收下", "release_id", cfg.ReleaseID, "generation", cfg.Generation,
			"下一轮补报", !failed.settled, "err", err)
	}
}

// reportSettled 判断这次上报之后还要不要再发。
//
// 面板回了 4xx（408 / 429 除外）说明请求送到了，重发同一份证据只会再被拒——
// 典型是重启后同一发布的 failed 已在库里、detail 却不同，面板回 409。传输
// 错误与 5xx / 408 / 429 是暂时的，留给下一轮拉取顺带补报。
func reportSettled(err error) bool {
	if err == nil {
		return true
	}
	var status *panel.StatusError
	if !errors.As(err, &status) {
		return false
	}
	return status.Code >= 400 && status.Code < 500 &&
		status.Code != http.StatusRequestTimeout && status.Code != http.StatusTooManyRequests
}

// ---------------------------------------------------------------------------
// 已应用版本与生效回执
// ---------------------------------------------------------------------------

func (n *Node) signedConfigAlreadyApplied(cfg *panel.SignedConfig) bool {
	if cfg.ConfigContract != "" {
		return cfg.ReleaseID == n.appliedReleaseID && cfg.Generation == n.appliedGeneration &&
			cfg.ContentSHA256 == n.appliedConfigHash
	}
	return cfg.Version == n.appliedConfigVersion && cfg.Hash == n.appliedConfigHash
}

func (n *Node) recordAppliedSignedConfig(cfg *panel.SignedConfig) {
	n.appliedConfigHash = cfg.Hash
	if cfg.ConfigContract != "" {
		n.appliedReleaseID = cfg.ReleaseID
		n.appliedGeneration = cfg.Generation
		n.appliedConfigVersion = 0
		n.appliedAt = time.Now()
		return
	}
	n.appliedConfigVersion = cfg.Version
	n.appliedReleaseID = ""
	n.appliedGeneration = 0
	n.appliedAt = time.Time{}
}

func (n *Node) effectiveHealthReady(now time.Time) bool {
	if !n.started || n.appliedAt.IsZero() || now.Sub(n.appliedAt) < effectiveHealthStabilityWindow {
		return false
	}
	probe, ok := n.kernel.(core.InboundReadiness)
	return ok && probe.InboundReady(n.tag) == nil
}

func (n *Node) reportSignedConfigPhase(ctx context.Context, cfg *panel.SignedConfig, phase, detail string) error {
	detail = reportDetail(detail)
	if cfg != nil && cfg.ConfigContract != "" {
		return n.signed.ReportEffectiveConfig(ctx, cfg, phase, detail)
	}
	if cfg == nil {
		return fmt.Errorf("signed config is required")
	}
	return n.signed.ReportConfig(ctx, cfg.Version, phase, detail)
}

// reportDetail 把详情截到面板上限以内（按 UTF-8 字符边界）。内核的回滚错误
// 是多段 errors.Join，超长的话整条上报被拒，面板就永远不知道为什么失败。
func reportDetail(detail string) string {
	if len(detail) <= maxReportDetailBytes {
		return detail
	}
	cut := maxReportDetailBytes
	for cut > 0 && !utf8.RuneStart(detail[cut]) {
		cut--
	}
	return detail[:cut]
}
