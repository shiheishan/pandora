package nodefabric

import (
	"time"
)

// ============================================================
//  后台节点列表与详情：下发与运行的真实状态
// ============================================================
//
// 审计 multinode 第 4 节：生效回执（node_config_applications）与 desired / applied 的
// 生效版本早已入库，却没有任何读接口；前端「配置版本」读的是旧的整数版本，签名节点永远
// 显示「—」。这里把它们与 pdnd 上报的运行原因一起交给后台。

// 生效状态（EffectiveState）。
const (
	// EffectiveNone：节点还没拉过生效配置（兼容通道节点、新建未接入）
	EffectiveNone = ""
	// EffectiveApplied：已应用的就是期望的版本
	EffectiveApplied = "applied"
	// EffectivePending：期望的版本还没回执成功，最近一条回执也不是失败
	EffectivePending = "pending"
	// EffectiveFailed：期望的版本没生效，且最近一条回执是失败
	EffectiveFailed = "failed"
)

// ApplyFailure 是最近一条生效回执（失败的那条）。
type ApplyFailure struct {
	// Phase 是回执阶段：failed / precheck_failed / health_failed / rolled_back
	Phase string `json:"phase"`
	// Detail 是节点给的原因原文（如「bind: address already in use」）
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
	// Generation 是这次失败对应的生效版本
	Generation *int64 `json:"generation"`
}

// NodeRuntimeView 是节点列表行里的下发与运行状态。
type NodeRuntimeView struct {
	DesiredEffectiveGeneration *int64 `json:"desired_effective_generation"`
	AppliedEffectiveGeneration *int64 `json:"applied_effective_generation"`
	// EffectiveState 见上方常量；前端据它显示「运行中 / 待生效 / 生效失败」
	EffectiveState string `json:"effective_state"`
	// LastApplyFailure 只在最近一条回执是失败时给出
	LastApplyFailure *ApplyFailure `json:"last_apply_failure"`
	// RuntimeStatus / RuntimeReason 是节点上报的运行状态与原因码（老节点不报为 null）
	RuntimeStatus  *string    `json:"runtime_status"`
	RuntimeReason  *string    `json:"runtime_reason"`
	RuntimeStateAt *time.Time `json:"runtime_state_at"`
	// RuntimeReasonNode 是端口冲突原因里那个占用者（同租户的节点）的名字，前端拼「被节点 X 占用」
	RuntimeReasonNode *string `json:"runtime_reason_node"`
	// DeliveryDegraded 与订阅降级同一口径（RuntimeFailingSQL）：为 true 时只有套餐里没有
	// 别的可用节点才会下发它
	DeliveryDegraded bool `json:"delivery_degraded"`
}

// applyFailurePhases 是算作「生效失败」的回执阶段，与 RuntimeFailingSQL 同一组。
var applyFailurePhases = map[string]bool{
	"failed": true, "precheck_failed": true, "health_failed": true, "rolled_back": true,
}

// portInUseOwnerUUID 是端口冲突原因里占用者节点 id 的形状；不是这个形状（other）不去查。
// CASE 保证只有匹配时才做 ::uuid 转换。
const portInUseOwnerUUID = `'^port_in_use:[0-9]{1,5}/(tcp|udp):[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'`

// nodeRuntimeViewColumns 接在节点列表 SELECT 的末尾（别名 n 是 nodes），与 nodeRuntimeViewScan.dest 一一对应。
// 最后一列与订阅降级同一口径（RuntimeFailingSQL）。
var nodeRuntimeViewColumns = `n.desired_effective_generation, n.applied_effective_generation,
				       (n.desired_effective_release_id, n.desired_effective_generation)
				         IS NOT DISTINCT FROM (n.applied_effective_release_id, n.applied_effective_generation),
				       lr.phase, lr.message, lr.occurred_at, lr.generation,
				       n.runtime_status, n.runtime_reason, n.runtime_state_at,
				       ro.name,
				       ` + RuntimeFailingSQL("n")

// nodeRuntimeViewJoins 接在节点列表 FROM 的末尾：最近一条生效回执、端口冲突原因里的占用者。
// 都按本页节点逐行查索引（回执 (node_id, occurred_at DESC)），不扫全表。
const nodeRuntimeViewJoins = `LEFT JOIN LATERAL (
				        SELECT a.phase, a.detail ->> 'message' AS message, a.occurred_at,
				               a.effective_generation AS generation
				          FROM node_config_applications a
				         WHERE a.tenant_id = n.tenant_id AND a.node_id = n.id
				         ORDER BY a.occurred_at DESC LIMIT 1) lr ON true
				  LEFT JOIN LATERAL (
				        SELECT o.name FROM nodes o
				         WHERE o.tenant_id = n.tenant_id
				           AND o.id = CASE WHEN n.runtime_reason ~ ` + portInUseOwnerUUID + `
				                           THEN split_part(n.runtime_reason, ':', 3)::uuid END) ro ON true`

// nodeRuntimeViewScan 接住 nodeRuntimeViewColumns 的各列。
type nodeRuntimeViewScan struct {
	desired, applied *int64
	effApplied       bool
	phase, message   *string
	at               *time.Time
	generation       *int64
	status, reason   *string
	stateAt          *time.Time
	reasonNode       *string
	failing          bool
}

func (r *nodeRuntimeViewScan) dest() []any {
	return []any{&r.desired, &r.applied, &r.effApplied, &r.phase, &r.message, &r.at, &r.generation,
		&r.status, &r.reason, &r.stateAt, &r.reasonNode, &r.failing}
}

func (r *nodeRuntimeViewScan) view() NodeRuntimeView {
	v := NodeRuntimeView{
		DesiredEffectiveGeneration: r.desired, AppliedEffectiveGeneration: r.applied,
		RuntimeStatus: r.status, RuntimeReason: r.reason, RuntimeStateAt: r.stateAt,
		RuntimeReasonNode: r.reasonNode, DeliveryDegraded: r.failing,
	}
	failed := r.phase != nil && applyFailurePhases[*r.phase]
	if failed && r.at != nil {
		v.LastApplyFailure = &ApplyFailure{Phase: *r.phase, Detail: value(r.message), At: *r.at, Generation: r.generation}
	}
	switch {
	case r.desired == nil:
		v.EffectiveState = EffectiveNone
	case r.effApplied:
		v.EffectiveState = EffectiveApplied
	case failed:
		v.EffectiveState = EffectiveFailed
	default:
		v.EffectiveState = EffectivePending
	}
	return v
}
