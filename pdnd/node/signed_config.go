// [INPUT]: 依赖 panel 的 SignedClient（ReportConfig / ReportEffectiveConfig）与 SignedConfig，依赖 core 的 InboundReadiness
// [OUTPUT]: 对内提供 signedConfigAlreadyApplied、recordAppliedSignedConfig、effectiveHealthReady、reportSignedConfigPhase 与生效健康窗口常量
// [POS]: pdnd/node 的签名通道配置台账：记下已应用的版本（release/generation/内容哈希或旧式 version/hash），按版本幂等上报 switched / health_passed / failed；主循环与 syncConfig 在 node.go

package node

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

const effectiveHealthStabilityWindow = 5 * time.Second

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
	if cfg != nil && cfg.ConfigContract != "" {
		return n.signed.ReportEffectiveConfig(ctx, cfg, phase, detail)
	}
	if cfg == nil {
		return fmt.Errorf("signed config is required")
	}
	return n.signed.ReportConfig(ctx, cfg.Version, phase, detail)
}
