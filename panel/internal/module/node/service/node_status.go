package service

// 节点服务状态与服务器状态的转换表。
//
// 节点只有一套服务状态 serving_status（旧的 17 状态接入流水线已删）。
// 服务器的边 = 旧代码的边去掉 unhealthy / quarantined，再加 ready→maintenance、maintenance→draining。
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
