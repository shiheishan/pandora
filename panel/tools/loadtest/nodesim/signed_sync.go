package nodesim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

// 签名通道的一轮配置同步（current），逐段对齐新 pdnd 的 syncSignedConfig
// （pdnd/node/signed_config.go）：
//
//   - 节点在服务、手上是生效发布时，把已应用的 release_id/generation 报给面板；
//     面板回 204 即仍是当前版，这一轮只补报还没被面板收下的 switched / health_passed；
//   - 回执被面板收下（2xx）或明确拒收（4xx，408 / 429 除外）之后不再重报，
//     送不到（传输错误、5xx、408、429）才留给下一轮；配置版本变了重新计；
//   - 验签失败先强制问一次换钥再验，换钥检查平时十分钟一次（signedClient）。
//
// pdnd 的 failedSigned（同一个装不上的版本只报一次 failed、旧配置仍在服务时不再试装）
// 不模拟：模拟节点的应用只在端口非法或载荷不是 JSON 时失败，面板正常下发时不会发生。
func (n *simNode) syncSigned(ctx context.Context, flag string) error {
	cfg, unchanged, err := n.signed.configSince(ctx, n.appliedRelease(), flag)
	if err != nil {
		return err
	}
	if unchanged {
		return n.settleAppliedReports(ctx)
	}
	if err := n.verifySigned(ctx, cfg, flag); err != nil {
		n.obs.fleet.verifyFailures.Add(1)
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if n.started.Load() && n.alreadyApplied(cfg) {
		return n.settleAppliedReports(ctx)
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(cfg.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	if err := n.applyConfig(raw); err != nil {
		_ = n.signed.reportPhase(ctx, cfg, "failed", err.Error())
		return err
	}
	n.recordApplied(cfg)
	err = n.signed.reportPhase(ctx, cfg, "switched", "")
	n.switchedSettled = reportSettled(err)
	if err != nil {
		n.obs.sample("switched report", n.id, err)
	}
	return nil
}

// verifySigned 对应 pdnd verifySignedConfig：验不过时先强制问一次面板换没换签名密钥
// 再验。换钥检查不再每轮都做，面板刚轮换密钥时新发布会先于下一次例行检查到达。
func (n *simNode) verifySigned(ctx context.Context, cfg *nodefabric.SignedConfig, flag string) error {
	err := n.signed.verifyConfig(cfg)
	if err == nil {
		return nil
	}
	n.obs.fleet.forcedKeyChecks.Add(1)
	if refreshErr := n.signed.refreshConfigKey(ctx, flag); refreshErr != nil {
		return errors.Join(err, refreshErr)
	}
	return n.signed.verifyConfig(cfg)
}

// appliedRelease 是报给面板的已应用版本（pdnd appliedRelease），格式与
// nodefabric.AppliedEffectiveReleaseHeader 一致："<release_id>/<generation>"。
// 节点没在服务、或手上是旧式签名配置（没有 release_id）时不报，拿全量。
func (n *simNode) appliedRelease() string {
	if !n.started.Load() || n.appliedReleaseID == "" || n.appliedGeneration == 0 {
		return ""
	}
	return n.appliedReleaseID + "/" + strconv.FormatUint(n.appliedGeneration, 10)
}

// settleAppliedReports 补报已应用版本还没被面板收下的阶段（pdnd 同名函数）。
// 补报失败返回错误，与 pdnd 一样让这一轮跳过用户同步。
func (n *simNode) settleAppliedReports(ctx context.Context) error {
	cfg := n.appliedSigned
	if cfg == nil {
		return nil
	}
	if !n.switchedSettled {
		err := n.signed.reportPhase(ctx, cfg, "switched", "")
		if !reportSettled(err) {
			return fmt.Errorf("retry effective config switched report: %w", err)
		}
		n.switchedSettled = true
	}
	if n.healthSettled || !n.healthReady(time.Now()) {
		return nil
	}
	err := n.signed.reportPhase(ctx, cfg, "health_passed", "")
	if !reportSettled(err) {
		return fmt.Errorf("retry effective config health report: %w", err)
	}
	n.healthSettled = true
	return nil
}

// reportSettled 对应 pdnd 同名函数：面板回了 4xx（408 / 429 除外）说明请求送到了，
// 重发同一份证据只会再被拒；传输错误与 5xx / 408 / 429 是暂时的，下一轮再报。
func reportSettled(err error) bool {
	if err == nil {
		return true
	}
	var status *statusError
	if !errors.As(err, &status) {
		return false
	}
	return status.code >= 400 && status.code < 500 &&
		status.code != http.StatusRequestTimeout && status.code != http.StatusTooManyRequests
}
