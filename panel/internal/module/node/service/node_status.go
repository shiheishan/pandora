package service

// 节点服务状态与服务器状态的转换表。
//
// 节点只有一套服务状态 serving_status，边表在 nodeServingTransitions；服务器的边表在 serverStatusTransitions。
// retired 是终态；同状态不算转换（CanXxx(a, a) 为 false）。

import "slices"

// 节点服务状态。
const (
	NodeDraft    = "draft"
	NodeActive   = "active"
	NodeDisabled = "disabled"
	NodeRetired  = "retired"
)

// nodeServingTransitions 是节点服务状态允许的转换。
var nodeServingTransitions = map[string][]string{
	NodeDraft:    {NodeActive, NodeRetired},
	NodeActive:   {NodeDisabled, NodeRetired},
	NodeDisabled: {NodeActive, NodeRetired},
	NodeRetired:  {},
}

// CanNodeTransition 报告节点服务状态能否从 from 转到 to。
func CanNodeTransition(from, to string) bool {
	return slices.Contains(nodeServingTransitions[from], to)
}

// ValidNodeServingStatus 报告 s 是不是合法的节点服务状态。
func ValidNodeServingStatus(s string) bool {
	_, ok := nodeServingTransitions[s]
	return ok
}

// 服务器状态。
const (
	ServerDraft       = "draft"
	ServerReady       = "ready"
	ServerDraining    = "draining"
	ServerMaintenance = "maintenance"
	ServerRetired     = "retired"
)

// serverStatusTransitions 是服务器状态允许的转换。
var serverStatusTransitions = map[string][]string{
	ServerDraft:       {ServerReady, ServerMaintenance, ServerRetired},
	ServerReady:       {ServerDraining, ServerMaintenance},
	ServerDraining:    {ServerReady, ServerMaintenance, ServerRetired},
	ServerMaintenance: {ServerReady, ServerDraining, ServerRetired},
	ServerRetired:     {},
}

// CanServerTransition 报告服务器状态能否从 from 转到 to。
func CanServerTransition(from, to string) bool {
	return slices.Contains(serverStatusTransitions[from], to)
}

// ValidServerStatus 报告 s 是不是合法的服务器状态。
func ValidServerStatus(s string) bool {
	_, ok := serverStatusTransitions[s]
	return ok
}

// nodeStatusLabels、serverStatusLabels 是状态的中文名（与后台页面一致），只给面向管理员的错误文案用；
// 错误码与 fields 里仍是英文状态码。
var (
	nodeStatusLabels = map[string]string{
		NodeDraft: "草稿", NodeActive: "在役", NodeDisabled: "已停用", NodeRetired: "已退役",
	}
	serverStatusLabels = map[string]string{
		ServerDraft: "草稿", ServerReady: "服务中", ServerDraining: "维护中", ServerMaintenance: "停机维护", ServerRetired: "已退役",
	}
)

// statusLabel 取状态的中文名；表里没有的原样返回。
func statusLabel(labels map[string]string, status string) string {
	if l, ok := labels[status]; ok {
		return l
	}
	return status
}
