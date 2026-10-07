package nodefabric

import (
	"strings"
)

// ============================================================
//  节点上报的真实运行状态（pdnd w4pdnd 起如实上报）
// ============================================================
//
// 原先 pdnd 无条件报 running，入站没起来、新配置装不上的节点在后台显示在线、健康 90，
// 照常进订阅（审计 multinode 第 4 节）。现在 pdnd 在降级时报 degraded，并经请求头带上
// 机器可读的原因：
//
//	签名心跳        正文 runtime_status，请求头 X-Node-Runtime-Reason
//	兼容通道 /status 请求头 X-Node-Runtime-Status 与 X-Node-Runtime-Reason
//
// 原因不进正文：面板按 DisallowUnknownFields 解码，正文多一个字段整条 400。
// 状态与原因存进 nodes.runtime_status / runtime_reason（00122），心跳每次写同样的值；
// 真变化时 runtime_state_at 才前进、变更通知才发。

const (
	RuntimeStatusHeader = "X-Node-Runtime-Status"
	RuntimeReasonHeader = "X-Node-Runtime-Reason"

	RuntimeRunning  = "running"
	RuntimeDegraded = "degraded"

	// 原因码（pdnd node/status.go）。端口冲突是 port_in_use:<端口>/<tcp|udp>:<节点 id|other>。
	RuntimeReasonNotStarted    = "not_started"
	RuntimeReasonApplyFailed   = "config_apply_failed"
	RuntimeReasonServingCached = "serving_cached_config"
	RuntimeReasonPortInUse     = "port_in_use"

	// maxRuntimeReasonBytes 与 pdnd 同一上限，也是 00122 的 CHECK。
	maxRuntimeReasonBytes = 200
)

// RuntimeState 是一次上报的运行状态。Status 为空表示老节点没报（不知道）。
type RuntimeState struct {
	Status string
	Reason string
}

// NormalizeRuntimeState 把上报值规整成能入库的形状：
//   - 状态：空串保持空（老节点不报）；running 原样；其余一律算 degraded（与健康分口径一致：
//     不是 running 就降分）；
//   - 原因：只留可打印 ASCII、截到 200 字节；running 时清空（恢复了就不再挂着旧原因）。
func NormalizeRuntimeState(status, reason string) RuntimeState {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "", RuntimeRunning:
	default:
		status = RuntimeDegraded
	}
	if status == RuntimeRunning || status == "" {
		// 老节点不报状态时也不收原因：原因只对 degraded 有意义
		return RuntimeState{Status: status}
	}
	return RuntimeState{Status: status, Reason: asciiRuntimeReason(reason)}
}

func asciiRuntimeReason(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxRuntimeReasonBytes; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return strings.TrimSpace(b.String())
}

// HealthScore 是粗粒度健康分：能上报即 90，降级降到 40（POOL-004 的多维评分留到调度实装时再细化）。
func (r RuntimeState) HealthScore() int {
	if r.Status == RuntimeDegraded {
		return 40
	}
	return 90
}

// runtimeStateSetSQL 是心跳与 /status 共用的 SET 片段：$s、$r 是规整后的状态与原因（空串存 NULL），
// 只有任一变了 runtime_state_at 才前进。SET 右边引用的是更新前的值。
func runtimeStateSetSQL(statusParam, reasonParam string) string {
	return `runtime_status = nullif(` + statusParam + `,''),
			       runtime_reason = nullif(` + reasonParam + `,''),
			       runtime_state_at = CASE
			         WHEN runtime_status IS DISTINCT FROM nullif(` + statusParam + `,'')
			           OR runtime_reason IS DISTINCT FROM nullif(` + reasonParam + `,'')
			         THEN now() ELSE runtime_state_at END`
}

// RuntimeFailingSQL 是「节点自己报告或回执表明它的入站没按期望在服务」的唯一口径，订阅降级
// （subscription.preferFreshNodes 一类）与后台节点列表共用。alias 只接受本包与调用方写死的别名。
//
// 片段自带 coalesce，恒为 true / false（运行原因为 NULL 时不会变成 NULL）。两条任一成立：
//   - 运行原因是端口被占（port_in_use:…）或入站没起来（not_started）；
//   - 期望的生效版本与已应用的不同，且这个节点最近一条生效回执是失败（failed / precheck_failed /
//     health_failed / rolled_back）。只在期望与已应用不同时才去查回执，常态下不碰回执表。
func RuntimeFailingSQL(alias string) string {
	switch alias {
	case "n":
	default:
		panic("unsupported runtime failing SQL alias")
	}
	return `coalesce((starts_with(` + alias + `.runtime_reason, 'port_in_use:') OR ` + alias + `.runtime_reason = 'not_started'
	   OR (` + alias + `.desired_effective_generation IS NOT NULL
	       AND (` + alias + `.desired_effective_release_id, ` + alias + `.desired_effective_generation)
	           IS DISTINCT FROM (` + alias + `.applied_effective_release_id, ` + alias + `.applied_effective_generation)
	       AND (SELECT a.phase FROM node_config_applications a
	             WHERE a.tenant_id = ` + alias + `.tenant_id AND a.node_id = ` + alias + `.id
	             ORDER BY a.occurred_at DESC LIMIT 1)
	           IN ('failed', 'precheck_failed', 'health_failed', 'rolled_back'))), false)`
}
