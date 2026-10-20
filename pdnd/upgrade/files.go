package upgrade

// Request 是 StateDir/request 文件的 JSON 结构：
// 节点进程在校验并暂存完新二进制与清单后最后写入此文件，systemd 的 path 单元监听该文件创建并触发升级助手。
type Request struct {
	JobID            string `json:"job_id"`
	ConfirmWithinSec uint32 `json:"confirm_within_sec"`
}

// Result 是 StateDir/result 文件的 JSON 结构：
// 升级助手在失败或回滚时写入，节点进程在向面板上报 UpgradeReport 或在 Hello 中携带后删除。
// Phase 字段为 PhaseFailed 或 PhaseRolledBack。
type Result struct {
	JobID      string `json:"job_id"`
	Phase      string `json:"phase"`
	ReasonCode string `json:"reason_code"`
	Detail     string `json:"detail"`
}

// Marker 是 StateDir/applying（助手开始替换前写入）与 StateDir/healthy（新进程拿到 Welcome 后写入）共用的格式。
// 助手比对两者中的 JobID 与 SHA256 确认新版本是否已健康接管。
type Marker struct {
	JobID  string `json:"job_id"`
	SHA256 string `json:"sha256"`
}
