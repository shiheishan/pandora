package node

import (
	"errors"
	"strings"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// 运行状态如实上报。
//
// 心跳里每个节点一条 NodeRuntime，按节点的真实处境报：入站没起来、或新配置装不上停在旧配置、
// 或面板还没确认正靠落盘缓存服务的节点，不能在后台看起来和好节点一模一样。
//
//	RUNNING   入站在服务，面板最新确认过的配置已装上
//	DEGRADED  新配置装不上、旧配置仍在服务（config_apply_failed 或具体原因）；
//	          面板还没确认、正靠落盘缓存服务（serving_cached_config）
//	DOWN      入站没起来（not_started 或具体原因）
//
// 原因是纯 ASCII 的机器可读串。端口冲突时内核给 port_in_use:<端口>/<tcp|udp>:<占用者节点 id 或 other>，
// 上报时拆成 reason（port_in_use:<端口>/<L4>）与 reason_node_id。

const (
	reasonNotStarted    = "not_started"
	reasonApplyFailed   = "config_apply_failed"
	reasonServingCached = "serving_cached_config"
	reasonPortInUse     = "port_in_use"

	// maxRuntimeReasonBytes 限制原因串长度：面板只拿来展示与归类。
	maxRuntimeReasonBytes = 200
)

// runtime 是这个节点要上报的运行状态。
func (n *Node) runtime() *nodev1.NodeRuntime {
	rt := &nodev1.NodeRuntime{NodeId: n.id}
	var reason string
	switch {
	case !n.started:
		rt.Status = nodev1.RuntimeStatus_RUNTIME_STATUS_DOWN
		reason = applyErrReason(n.lastApplyErr, reasonNotStarted)
	case n.failed != nil:
		rt.Status = nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED
		reason = applyErrReason(n.lastApplyErr, reasonApplyFailed)
	case n.fromCache:
		rt.Status = nodev1.RuntimeStatus_RUNTIME_STATUS_DEGRADED
		reason = reasonServingCached
	default:
		rt.Status = nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING
	}
	rt.Reason, rt.ReasonNodeId = splitPortReason(reason)
	return rt
}

// applyErrReason 取内核给的机器可读原因（如端口占用），没有就用 fallback。
func applyErrReason(err error, fallback string) string {
	var r interface{ RuntimeReason() string }
	if err != nil && errors.As(err, &r) {
		if reason := asciiReason(r.RuntimeReason()); reason != "" {
			return reason
		}
	}
	return fallback
}

// asciiReason 只留可打印 ASCII 并截断。
func asciiReason(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxRuntimeReasonBytes; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitPortReason 把 port_in_use:443/tcp:<节点 id> 拆成 port_in_use:443/tcp 与占用者节点 id
// （占用者是 other，即本机别的进程时为空）；其他原因原样返回。
func splitPortReason(reason string) (code, nodeID string) {
	if !strings.HasPrefix(reason, reasonPortInUse+":") {
		return reason, ""
	}
	rest := reason[len(reasonPortInUse)+1:]
	port, owner, ok := strings.Cut(rest, ":")
	if !ok {
		return reason, ""
	}
	code = reasonPortInUse + ":" + port
	if owner == "other" {
		return code, ""
	}
	return code, owner
}

// sendHeartbeat 报本机资源占用与各节点运行状态。面板拿它判断「这台机器还活着吗」与各节点健康。
// 没连着就不采样（省掉 CPU 采样的 100ms）；失败不重试，下个节拍再来。
func (s *Server) sendHeartbeat() {
	if !s.connected || s.link == nil {
		return
	}
	hb := &nodev1.Heartbeat{Metrics: s.metric()}
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil {
			continue
		}
		rt := n.runtime()
		if rt.Status != nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING {
			n.log.Warn("节点降级运行", "状态", rt.Status.String(), "原因", rt.Reason, "err", n.lastApplyErr)
		}
		hb.Nodes = append(hb.Nodes, rt)
	}
	if !s.link.SendHeartbeat(hb) {
		s.log.Debug("心跳没发出去，等下个节拍")
	}
}
