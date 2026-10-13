package service

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/node/repo"
)

// nodeDeliveryFreshWindow 决定用户能不能拿到线路的心跳窗口，刻意比后台 90 秒的 stale 宽。
const nodeDeliveryFreshWindow = 10 * time.Minute

const (
	nodeNoteNotActive      = "服务状态不是 active，不下发"
	nodeNoteNoPool         = "未划入节点池，不服务任何用户"
	nodeNotePoolInactive   = "所在节点池未启用，不下发"
	nodeNoteNeverSeen      = "所在服务器从未上报过心跳，不下发 —— 多半是还没在机器上安装节点端。发出去就是一条必然连不上的线路"
	nodeNoteServerNotReady = "所属服务器未就绪或已删除，不下发"
	nodeNoteNodeNotReady   = "协议、端口或可连地址不完整，不下发"
	nodeNoteStale          = "心跳已超时，但仍在下发 —— 只有当套餐里一个新鲜节点都没有时才会用到它。空订阅会让客户端清空服务器列表，比给一条可能不通的线路更糟"
	nodeNoteDegraded       = "节点报告运行异常（端口被占、入站没起来或配置生效失败），只有当套餐里没有其他可用节点时才会下发它"
)

// nodeDeliveryFacts 是判定「是否下发」时用的输入事实。
type nodeDeliveryFacts struct {
	Serving     string
	Pooled      bool
	PoolActive  bool
	EverSeen    bool
	BeatFresh   bool
	Deliverable bool
	ServerReady bool
	Degraded    bool
}

// nodeDeliveryVerdict 按顺序判定节点会不会出现在用户订阅里及说明文案。
// Deliverable 来自 repo.DeliverableNodeCond（订阅、套餐页、后台三处同一口径）；「池没绑套餐」第二波补。
func nodeDeliveryVerdict(f nodeDeliveryFacts) (bool, string) {
	if f.Serving != "active" {
		return false, nodeNoteNotActive
	}
	if !f.Pooled {
		return false, nodeNoteNoPool
	}
	if !f.PoolActive {
		return false, nodeNotePoolInactive
	}
	if !f.EverSeen {
		return false, nodeNoteNeverSeen
	}
	if !f.Deliverable {
		if f.ServerReady {
			return false, nodeNoteNodeNotReady
		}
		return false, nodeNoteServerNotReady
	}
	if !f.BeatFresh {
		return true, nodeNoteStale
	}
	if f.Degraded {
		return true, nodeNoteDegraded
	}
	return true, ""
}

// nodeEffectiveState 从列表行推出配置生效状态；pending 要等第二波有期望版本。
func nodeEffectiveState(r repo.NodeListRow) (string, *ApplyFailure) {
	if r.ApplyFailedAt != nil {
		detail := r.ApplyFailedReason
		if r.ApplyFailedDetail != nil && *r.ApplyFailedDetail != "" {
			detail = r.ApplyFailedDetail
		}
		d := ""
		if detail != nil {
			d = *detail
		}
		return "failed", &ApplyFailure{Phase: "failed", Detail: d, At: *r.ApplyFailedAt}
	}
	if r.AppliedConfigSHA256 != nil {
		return "applied", nil
	}
	return "", nil
}

// nodeDegraded 表示节点报告运行异常或最近一次配置应用失败。
func nodeDegraded(r repo.NodeListRow) bool {
	if r.ApplyFailedAt != nil {
		return true
	}
	if r.RuntimeStatus == nil {
		return false
	}
	st := *r.RuntimeStatus
	return st == "degraded" || st == "down"
}
