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
	// attempts / nextRetry 是旧配置仍在服务时的退避重试节拍（applyRetryDelay）。
	attempts  int
	nextRetry time.Time
	// detail 是首次失败的原因，补报时原样重发：生效回执按 report_id 去重，
	// 同一 report_id 带不同 detail 会被面板当成冲突的证据拒收。
	detail string
	// settled 为真表示面板已收下这条 failed，或已明确拒收（4xx），不再上报。
	settled bool
}

// syncSignedConfig 是签名通道的一轮配置同步。
//
// 同一个装不上的版本分两种处境：
//   - 旧配置仍在服务（整版回滚成功，或内核 PreviousPreserved）：按退避重试
//     （1、2、4 分钟，封顶 5 分钟，applyRetryDelay），没到点就不试。整版回滚的
//     内核每试一次就是两次入站重建，全部在线连接断两次，不能每轮都试；但也不能
//     永不重试——端口一度被占、随后释放这类故障（审计 E2）要能自己恢复。没到点
//     时返回 nil；用户同步不受配置成败影响（syncOnceErr）。
//   - 节点已停（首个配置就没装上、回滚也失败、或进程刚重启）：每轮都重试。
//     没有旧配置可保，重试没有代价；端口暂被占用这类故障一消失就该恢复。
//     节拍就是拉取间隔，与兼容通道作废配置 ETag 的语义一致。
//
// 失败对每个版本只报一次（送不到就随后的拉取补报，面板明确拒收就作罢）；
// 面板另从心跳的 applied_effective_* 看出节点仍停在旧版本。
//
// 节点在服务时把已应用的版本报给面板（ConfigSince）：仍是当前版，面板回 204，
// 这一轮只补报还没被面板收下的 switched / health_passed。
func (n *Node) syncSignedConfig(ctx context.Context) error {
	cfg, unchanged, err := n.signed.ConfigSince(ctx, n.appliedRelease())
	if err != nil {
		return err
	}
	if unchanged {
		return n.settleAppliedReports(ctx)
	}
	if err := n.verifySignedConfig(ctx, cfg); err != nil {
		n.reportSignedConfigPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if n.started && n.signedConfigAlreadyApplied(cfg) {
		return n.settleAppliedReports(ctx)
	}
	key := signedConfigKeyOf(cfg)
	if failed := n.failedSigned; n.started && failed != nil && failed.key == key {
		if n.clock().Before(failed.nextRetry) {
			n.reportSignedFailure(ctx, cfg)
			return nil
		}
		n.log.Info("重试之前装不上的配置", "release_id", cfg.ReleaseID, "generation", cfg.Generation,
			"第几次", failed.attempts+1)
	}
	if err := n.applySignedConfig(cfg); err != nil {
		if n.failedSigned == nil || n.failedSigned.key != key {
			n.failedSigned = &signedApplyFailure{key: key, detail: err.Error()}
		}
		n.failedSigned.attempts++
		n.failedSigned.nextRetry = n.clock().Add(applyRetryDelay(n.failedSigned.attempts))
		n.reportSignedFailure(ctx, cfg)
		return err
	}
	n.failedSigned = nil
	n.recordAppliedSignedConfig(cfg)
	n.saveSignedConfigCache(cfg)
	err = n.reportSignedConfigPhase(ctx, cfg, "switched", "")
	n.switchedSettled = reportSettled(err)
	if err != nil {
		n.log.Warn("配置已应用但切换上报失败", "release_id", cfg.ReleaseID, "generation", cfg.Generation, "err", err)
	}
	return nil
}

// verifySignedConfig 验签；验不过时先强制问一次面板换没换签名密钥再验。
//
// 换钥检查不再每轮都做（panel.SignedClient 十分钟一次），面板刚轮换密钥时新发布会
// 先于下一次例行检查到达——这里补上那一次检查，节点不会因此停在旧配置上。
func (n *Node) verifySignedConfig(ctx context.Context, cfg *panel.SignedConfig) error {
	err := n.signed.VerifyConfig(cfg)
	if err == nil {
		return nil
	}
	if refreshErr := n.signed.RefreshConfigSigningKey(ctx); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	return n.signed.VerifyConfig(cfg)
}

// appliedRelease 是报给面板的「已应用版本」。只有节点在服务、手上是生效发布
// （有 release_id）时才报：节点已停就必须拿全量重试，旧式签名配置没有这个身份。
func (n *Node) appliedRelease() *panel.AppliedRelease {
	if !n.started || n.appliedReleaseID == "" || n.appliedGeneration == 0 {
		return nil
	}
	return &panel.AppliedRelease{ReleaseID: n.appliedReleaseID, Generation: n.appliedGeneration}
}

// settleAppliedReports 补报已应用版本还没被面板收下的阶段。
//
// 原先每轮都把 switched、health_passed 重报一遍（靠 report_id 幂等），200 个节点
// 15 秒一轮，光这两条就占了节点请求的三分之一。现在面板收下（或明确拒收，4xx）
// 之后就不再报；送不到（传输错误、5xx、408、429）才留给下一轮。report_id 按发布
// 与阶段确定，补报照样幂等，修复「本地已切换、回执丢了」的能力不变。
func (n *Node) settleAppliedReports(ctx context.Context) error {
	cfg := n.appliedSigned
	if cfg == nil {
		return nil
	}
	if !n.switchedSettled {
		err := n.reportSignedConfigPhase(ctx, cfg, "switched", "")
		if !reportSettled(err) {
			return fmt.Errorf("retry effective config switched report: %w", err)
		}
		n.switchedSettled = true
	}
	if n.healthSettled || !n.effectiveHealthReady(time.Now()) {
		return nil
	}
	err := n.reportSignedConfigPhase(ctx, cfg, "health_passed", "")
	if !reportSettled(err) {
		return fmt.Errorf("retry effective config health report: %w", err)
	}
	n.healthSettled = true
	return nil
}

// applySignedConfig 解出已验签的 payload 交给 applyConfig。解析失败与应用失败
// 一样是这一版本身的问题，同一内容再解一次结果不会变。
func (n *Node) applySignedConfig(cfg *panel.SignedConfig) error {
	if cfg.TenantID != "" {
		n.tenantID = cfg.TenantID
	}
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
	n.appliedSigned = cfg
	n.switchedSettled, n.healthSettled = false, false
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
