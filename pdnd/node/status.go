package node

import (
	"context"
	"errors"
	"strings"

	"github.com/aegispanel/nodeagent/panel"
)

// 运行状态如实上报。
//
// 原先 reportStatus 无条件报 running，面板据此给健康分 90、照常进订阅——入站
// 根本没起来、或新配置装不上停在旧端口的节点，在后台看起来和好节点一模一样
// （审计 multinode 第 4 节）。现在按节点的真实处境报：
//
//	running   入站在服务，面板最新确认过的配置已装上
//	degraded  以下任一：入站没起来（not_started 或具体原因）；新配置装不上、
//	          旧配置仍在服务（config_apply_failed 或具体原因）；面板还没确认、
//	          正靠落盘缓存服务（serving_cached_config）
//
// 原因是纯 ASCII 的机器可读串，端口冲突时是内核给的
// port_in_use:<端口>/<tcp|udp>:<同面板节点 ID 或 other>。

const (
	runtimeRunning  = "running"
	runtimeDegraded = "degraded"

	reasonNotStarted    = "not_started"
	reasonApplyFailed   = "config_apply_failed"
	reasonServingCached = "serving_cached_config"
)

// maxRuntimeReasonBytes 限制原因串长度：它走请求头，面板侧也只拿来展示。
const maxRuntimeReasonBytes = 200

// runtimeHealth 返回要上报的运行状态与原因（running 时原因为空）。
func (n *Node) runtimeHealth() (status, reason string) {
	switch {
	case !n.started:
		return runtimeDegraded, applyErrReason(n.lastApplyErr, reasonNotStarted)
	case n.failedSigned != nil || n.compatFailure != nil:
		return runtimeDegraded, applyErrReason(n.lastApplyErr, reasonApplyFailed)
	case n.fromCache:
		return runtimeDegraded, reasonServingCached
	}
	return runtimeRunning, ""
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

// asciiReason 只留可打印 ASCII 并截断：原因要放进 HTTP 请求头。
func asciiReason(s string) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxRuntimeReasonBytes; i++ {
		if c := s[i]; c >= 0x20 && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// reportStatus 把本机资源占用与运行状态报给面板。
//
// 面板拿它更新 last_heartbeat_at 和 health_score——也就是后台节点列表上
// 「这个节点还活着吗」的唯一依据。不报的话那几列一直空着，服务挂了后台
// 也不变色，只能等用户报障。
//
// 两条通道都带资源占用，面板都写进 node_metrics（后台资源曲线读那张表）：
// 签名通道随 Heartbeat 带 metrics（另含负载、网络累计、TCP 连接数、开机
// 时长），兼容通道走 /status，只有 CPU、内存、磁盘。
//
// 原因在签名通道走请求头（心跳正文按 DisallowUnknownFields 解码，加字段会
// 整条 400），兼容通道放进 /status 正文（面板与 Xboard 都不拒未知字段）。
//
// 失败只记日志不重试：下一个节拍会再来一次，而卡在这里重试会挤掉同一个
// 循环里的配置同步和流量上报——那两件比状态上报重要。
func (n *Node) reportStatus(ctx context.Context) {
	status, reason := n.runtimeHealth()
	if status != runtimeRunning {
		n.log.Warn("节点降级运行", "原因", reason, "err", n.lastApplyErr)
	}
	if n.signed != nil {
		input := panel.HeartbeatInput{
			AgentVersion: "pandora-native", RuntimeVersion: n.kernel.Type(),
			RuntimeStatus: status, RuntimeReason: reason,
			ConfigVersion: n.appliedConfigVersion, ConfigSigningKeyID: n.signed.ConfigSigningKeyID(),
		}
		if n.appliedReleaseID != "" {
			input.AppliedReleaseID = n.appliedReleaseID
			input.AppliedGeneration = n.appliedGeneration
			input.AppliedContentSHA256 = n.appliedConfigHash
		} else {
			input.ConfigHash = n.appliedConfigHash
		}
		input.AttachHostMetrics()
		if _, err := n.signed.Heartbeat(ctx, input); err != nil {
			n.log.Error("签名上报运行状态失败", "err", err)
		}
		return
	}
	runtime := panel.CollectRuntimeStatus()
	runtime.Status, runtime.Reason = status, reason
	if err := n.client.Status(ctx, runtime); err != nil {
		n.log.Error("上报运行状态失败", "err", err)
	}
}
