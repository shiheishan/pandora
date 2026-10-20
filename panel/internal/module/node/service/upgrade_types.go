package service

// 节点在线升级（面板侧）的类型与常量：能力名、任务状态、原因码、会话里的升级事实、发布包给出的目标版本、
// 后台显示用的「能不能升级」与「最近一次任务」。流程在 upgrades.go，发布清单在 pdnd_manifest.go，
// 会话收发在 session_upgrade.go；两端共用的规格见 .claude/rules/module-node.md「在线升级（面板侧）」。

import (
	"time"

	"github.com/google/uuid"
)

// FeatureUpgradeV1 是「能收 Command.upgrade」的能力名（Hello.features / Welcome.features）。
// pdnd 只在 buildSerial > 0 且 root 侧助手已安装时声明。
const FeatureUpgradeV1 = "upgrade_v1"

// UpgradeConfirmWithinSec 是新进程连上面板的确认时限（Upgrade.confirm_within_sec），面板固定给 180 秒。
const UpgradeConfirmWithinSec = 180

// UpgradeDeadline 是任务从下发到判超时的时长：确认时限 + 10 分钟（下载与替换）。
const UpgradeDeadline = UpgradeConfirmWithinSec*time.Second + 10*time.Minute

// UpgradeBatchMax 是一次批量发起最多的服务器数。
const UpgradeBatchMax = 50

// 任务状态（server_upgrades.status）。sent、staged 是未结束，其余是结束态。
const (
	UpgradeSent       = "sent"
	UpgradeStaged     = "staged"
	UpgradeSucceeded  = "succeeded"
	UpgradeFailed     = "failed"
	UpgradeRolledBack = "rolled_back"
	UpgradeTimedOut   = "timed_out"
)

// 面板侧「不能在线升级」的原因码（后台按码出中文文案，表在前端 nodes/infra.ts）。
// 发布包级的四个（manifest_missing、signature_missing、manifest_invalid、binary_mismatch）也是 PdndTarget.Reason 的取值；
// manifest_invalid、serial_not_newer 与节点回报的同名码意思相同。not_connected 也写进任务的 reason_code（下发时会话已断）。
const (
	UpgradeBlockManifestMissing  = "manifest_missing"   // 发布包里没有 pdnd-manifest.json
	UpgradeBlockSignatureMissing = "signature_missing"  // 发布包里没有 pdnd-manifest.json.sig（或为空）
	UpgradeBlockManifestInvalid  = "manifest_invalid"   // 清单解析失败、schema 不对、字段不合法
	UpgradeBlockBinaryMismatch   = "binary_mismatch"    // 面板托管的二进制缺失或摘要与清单不符
	UpgradeBlockInProgress       = "in_progress"        // 这台已有一次升级没结束
	UpgradeBlockNotReady         = "not_ready"          // 服务器不是就绪状态
	UpgradeBlockNotConnected     = "not_connected"      // 节点端没连着面板
	UpgradeBlockNoSupport        = "no_upgrade_support" // 节点端没协商 upgrade_v1（旧版、开发构建或没装升级助手）
	UpgradeBlockArchUnsupported  = "arch_unsupported"   // 发布清单里没有这台的架构
	UpgradeBlockUpToDate         = "up_to_date"         // 已经是目标版本（摘要相同）
	UpgradeBlockSerialNotNewer   = "serial_not_newer"   // 目标版本的发版序号不比这台上的新
	UpgradeBlockNotFound         = "not_found"          // 服务器不存在或已删除（只出现在批量发起的结果里）
)

// UpgradePhase 是节点回报的升级阶段（与 proto UpgradePhase 同义）。
type UpgradePhase int

// 升级回报阶段。
const (
	UpgradePhaseStaged UpgradePhase = iota + 1
	UpgradePhaseFailed
	UpgradePhaseRolledBack
)

// UpgradeReport 是节点回报的一次升级进度或结局（流上的 UpgradeReport，或 Hello.last_upgrade）。
type UpgradeReport struct {
	JobID      uuid.UUID
	Phase      UpgradePhase
	ReasonCode string
	Detail     string
}

// UpgradeCommand 是发往节点的 Command.upgrade。
type UpgradeCommand struct {
	JobID uuid.UUID
	// Manifest、Signature 是发布清单与签名文件的原字节
	Manifest, Signature []byte
	BinaryURL           string
	ConfirmWithinSec    uint32
}

// SessionUpgradeInfo 是在途会话里与升级有关的 Hello 事实（建流时定下，会话期间不变）。
type SessionUpgradeInfo struct {
	// UpgradeV1：双方协商出了 upgrade_v1
	UpgradeV1 bool
	// BuildSerial 是节点二进制的发版序号（Hello.build_serial；开发构建为 0）
	BuildSerial uint64
	// Arch 是节点自报的架构（Hello.host.arch，即 runtime.GOARCH，已转小写去空白）
	Arch string
}

// PdndTarget 是面板托管的发布包给出的目标版本（pdnd_manifest.go 的 PdndTarget）。面板不验签，信任根在节点。
type PdndTarget struct {
	// Available 为假时 Reason 是发布包级原因码（manifest_missing / signature_missing / manifest_invalid / binary_mismatch）
	Available bool
	Reason    string
	// Version、Serial、Artifacts 在清单解析成功时才有值（Reason 为 binary_mismatch 时也有）
	Version string
	Serial  uint64
	// Artifacts 的键是架构键 "linux-" + GOARCH（如 linux-amd64）
	Artifacts map[string]PdndTargetArtifact
	// Manifest、Signature 是两个文件的原字节，下发时原样带上；调用方只读不改
	Manifest, Signature []byte
}

// PdndTargetArtifact 是清单里一个架构的产物。
type PdndTargetArtifact struct {
	// File 是产物名（白名单里的 pandora-native-linux-amd64 / -arm64）
	File string
	// SHA256 是清单里的摘要（64 位小写 hex）
	SHA256 string
	// Available：面板托管的同名二进制存在且摘要等于 SHA256；否则 Reason 为 binary_mismatch
	Available bool
	Reason    string
}

// ServerUpgradeOption 是一台服务器现在能不能在线升级（后台列表与详情）。
type ServerUpgradeOption struct {
	Available bool
	// TargetVersion 是目标版本；清单读不出来时为空
	TargetVersion string
	// Reason 是不能升级时的 UpgradeBlock* 原因码；能升级时为空
	Reason string
}

// ServerUpgradeJob 是一台服务器最近一次升级任务（后台列表与详情）。
type ServerUpgradeJob struct {
	ID            uuid.UUID
	Status        string
	TargetVersion string
	ReasonCode    *string
	// UpdatedAt 是 finished_at、staged_at、sent_at、created_at 里最晚的那个
	UpdatedAt time.Time
}

// UpgradeResult 是批量发起里一台服务器的结果。
type UpgradeResult struct {
	ServerID uuid.UUID
	OK       bool
	// Reason 是没发起或没发出去时的原因码（UpgradeBlock*）；OK 时为空
	Reason string
}

// UpgradeBatch 是一次批量发起的结果：Results 与请求里的服务器同序（去重后）。
type UpgradeBatch struct {
	TargetVersion string
	Results       []UpgradeResult
}
