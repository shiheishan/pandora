package bindingcontract

// 合并上报（S5）与事件流（S6）里的枚举。两端各写一份，金样本钉住取值与顺序。
// 新增取值必须先由面板在清单 features 里声明，pdnd 看到后才会发送（见合约文档「兼容与协商」）。

// 节点失败码：S5 nodes[].failure.code 与 nodes[].warnings[].code 的取值。
const (
	FailurePortInUse           = "port_in_use"
	FailurePortReserved        = "port_reserved"
	FailureCertMissing         = "cert_missing"
	FailureCertInvalid         = "cert_invalid"
	FailureCertExpiring        = "cert_expiring"
	FailureProtocolUnsupported = "protocol_unsupported"
	FailureConfigInvalid       = "config_invalid"
	FailureBindPermission      = "bind_permission"
)

// FailureCodes 是全部节点失败码。
var FailureCodes = []string{
	FailurePortInUse, FailurePortReserved, FailureCertMissing, FailureCertInvalid,
	FailureCertExpiring, FailureProtocolUnsupported, FailureConfigInvalid, FailureBindPermission,
}

// WarningOnlyCodes 只能出现在 warnings 里：节点照常运行，不进入 failed。
var WarningOnlyCodes = []string{FailureCertExpiring}

// RuntimeStates 是 S5 nodes[].runtime_state 的取值。
var RuntimeStates = []string{"running", "pending", "failed", "stopped"}

// ReceiptResults 是 S5 nodes[].receipt.result 的取值，与现有配置回执一致。
var ReceiptResults = []string{"switched", "health_passed", "failed"}

// UpgradeStates 是 S5 agent_upgrade.state 的取值。
var UpgradeStates = []string{"accepted", "downloading", "installing", "succeeded", "failed", "rolled_back"}

// UpgradeFailureCodes 是 S5 agent_upgrade.failure.code 的取值。
var UpgradeFailureCodes = []string{
	"upgrade_order_invalid",
	"upgrade_release_unavailable",
	"upgrade_signature_invalid",
	"upgrade_hash_mismatch",
	"upgrade_downgrade_refused",
	"upgrade_install_failed",
	"upgrade_rolled_back",
}

// EventTypes 是服务器事件流（S6）的事件名。事件只是「该拉了」的提示，不携带数据、
// 不签名；pdnd 收到任何事件都只是提前一次对应的签名拉取，不明事件忽略。
var EventTypes = []string{"sync.manifest", "sync.config", "users.delta", "users.full", "ping"}
