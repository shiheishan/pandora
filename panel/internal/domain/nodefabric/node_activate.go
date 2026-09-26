// [INPUT]: 依赖 node_refusal.go 的 NodeStatusRefusal（状态机报错翻译）、service.go 的 lockLegacyConfigRelease、node_admin.go 的 GetAdminNode / nodeVersionConflict / validateAdminUUID / StableProtocolReadySQL、server_admin.go 的 ValidServerStatusTransition，依赖 platform 的 db/audit/httpx；写 nodes、servers
// [OUTPUT]: 对外提供 ActivateNodeInput、ActivateNodeResult、Service.ActivateNode、ProjectNodeLifecycle
// [POS]: domain/nodefabric 的一步上线（契约后台-07 POST v1/nodes/{id}/activate，R108）：与 node_retire.go 对称，生命周期按 node_transitions 的合法边逐条推进到 active、服务状态按 ProjectNodeLifecycle 投影、服务器同事务进 ready
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ProjectNodeLifecycle 把节点生命周期投影到服务状态与宿主服务器状态，是旧的
// POST v1/nodes/{id}/status 与一步上线共用的唯一映射。canary 可服务（用于验证）
// 但订阅下发看的是 serving_status 与服务器，draining 保留数据面鉴权、停止新分配；
// 协议没就绪的节点不能被投影成可服务。
func ProjectNodeLifecycle(nodeStatus string, protocolReady bool) (servingStatus, serverStatus string) {
	var serving, server string
	switch nodeStatus {
	case "active", "canary":
		serving, server = "active", "ready"
	case "draining":
		serving, server = "draining", "draining"
	case "maintenance":
		serving, server = "disabled", "maintenance"
	case "unhealthy":
		serving, server = "disabled", "unhealthy"
	case "quarantined":
		serving, server = "disabled", "quarantined"
	case "retired", "destroyed":
		serving, server = "retired", "retired"
	default:
		serving, server = "draft", "draft"
	}
	if !protocolReady && (serving == "active" || serving == "draining") {
		serving = "disabled"
	}
	return serving, server
}

// activatePaths 是接入尾段各状态到 active 的合法路径（每一步都是 node_transitions
// 里的边，00005：唯一进入正式池的路径是 standby → canary → active）。
// 没列出的状态一律不上线：接入还没提交（draft / provisioning / bootstrapping）、
// 失败或终态（*_failed / quarantined / retired / destroyed），以及已经在役后又
// 离开的状态（draining / maintenance / unhealthy / upgrade_failed），后者走启用
// 或状态操作，不是「上线」。
var activatePaths = map[string][]string{
	"attesting":  {"installing", "validating", "standby", "canary", "active"},
	"installing": {"validating", "standby", "canary", "active"},
	"validating": {"standby", "canary", "active"},
	"standby":    {"canary", "active"},
	"canary":     {"active"},
}

type ActivateNodeInput struct {
	ID         string
	RowVersion int64
	ActorID    string
}

// ActivateNodeResult 是上线后的节点（AdminNode，带 warnings）。Changed 为 false
// 表示节点本来就是 active、什么都没改；ServerReady 表示这次顺带把服务器推进了 ready，
// 同服务器上其他节点的下发集合也因此变了，调用方据此决定通知范围。
type ActivateNodeResult struct {
	Node        *AdminNode
	Changed     bool
	ServerReady bool
}

// ActivateNode 在一个事务里把接入尾段的节点推到 active 并让它开始服务。
//
// 起因（R108）：接入流程只把节点推到 attesting，之后只有旧的逐级状态接口能往前推；
// 而批量启用要求服务器已 ready、服务器进 ready 又要求名下有 active 节点——新服务器
// 加新节点在新前端里上不了线。这里一步到位，每一步都过状态机触发器，不绕过。
//
// 前置条件（不满足回 409 并写明原因）：
//   - 处于接入尾段（attesting / installing / validating / standby / canary）；
//   - 有一份有效且未过期的节点身份（接入已提交）；
//   - 协议已就绪（有类型、端口合法、稳定协议且配置已校验），否则投影不出可服务；
//   - 绑着一台未删除的服务器，且服务器能按服务器状态机进 ready（已 ready 则不动）。
func (s *Service) ActivateNode(ctx context.Context, tenantID string, in ActivateNodeInput) (*ActivateNodeResult, error) {
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if validateAdminUUID("id", in.ID, true) != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	res := &ActivateNodeResult{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// 与配置发布、批量改状态、退役同一把锁：上线改服务状态与服务器状态，
		// 不能和一次正在进行的发布交错
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		var status, serving string
		var version int64
		var serverID *string
		var protocolReady, identity bool
		err := tx.QueryRow(ctx, `
			SELECT n.status, n.serving_status, n.row_version, n.server_id::text,
			       COALESCE(n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535
			                AND `+StableProtocolReadySQL("n")+`, false),
			       EXISTS (SELECT 1 FROM node_identities i
			                WHERE i.tenant_id = n.tenant_id AND i.node_id = n.id
			                  AND i.status = 'active' AND i.expires_at > now())
			  FROM nodes n
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid AND n.status <> 'destroyed'
			 FOR UPDATE OF n`, tenantID, in.ID).
			Scan(&status, &serving, &version, &serverID, &protocolReady, &identity)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		// 已经上线：幂等，什么都不改（重放、双击、别的管理员先点了）
		if status == "active" {
			return nil
		}
		if version != in.RowVersion {
			return nodeVersionConflict(version)
		}
		path, ok := activatePaths[status]
		if !ok {
			return httpx.New(httpx.CodeConflict, activateRefusal(status))
		}
		if !identity {
			return httpx.New(httpx.CodeConflict, "节点没有有效的节点身份，请重新接入后再上线")
		}
		if !protocolReady {
			return httpx.New(httpx.CodeConflict, "节点的协议配置还没就绪（协议类型、端口与稳定协议配置），先保存协议再上线")
		}
		if serverID == nil {
			return httpx.New(httpx.CodeConflict, "节点没有绑定服务器，不能上线")
		}
		var serverStatus string
		var serverVersion int64
		err = tx.QueryRow(ctx, `
			SELECT status, row_version FROM servers
			 WHERE tenant_id = $1 AND id = $2::uuid AND deleted_at IS NULL
			 FOR UPDATE`, tenantID, *serverID).Scan(&serverStatus, &serverVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeConflict, "节点所在的服务器已删除，不能上线")
		}
		if err != nil {
			return err
		}
		finalServing, targetServer := ProjectNodeLifecycle("active", protocolReady)
		if serverStatus != targetServer && !ValidServerStatusTransition(serverStatus, targetServer) {
			return httpx.New(httpx.CodeConflict,
				"节点所在的服务器处于 "+serverStatus+"，不能进入 ready，先处理服务器状态再上线")
		}

		for _, next := range path {
			if _, err := tx.Exec(ctx, `
				UPDATE nodes SET status = $3, entered_status_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, in.ID, next); err != nil {
				// 状态机触发器拒绝的跳转回 409（中文原样，约束英文原句只进日志）
				if db.IsCheckViolation(err) {
					return NodeStatusRefusal(err)
				}
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE nodes SET serving_status = $3, row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, in.ID, finalServing); err != nil {
			return err
		}
		// 服务器进 ready 的规则（SetServerStatus）：名下要有 active、可服务、协议就绪的节点。
		// 上面刚把这个节点推成了这样，同一事务里满足；已经 ready 的不动
		if serverStatus != targetServer {
			if _, err := tx.Exec(ctx, `
				UPDATE servers SET status = $3, status_reason = NULL, entered_status_at = now(),
				       retired_at = NULL, row_version = row_version + 1
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, *serverID, targetServer); err != nil {
				return err
			}
			res.ServerReady = true
		}
		res.Changed = true
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.activate", ResourceType: "node", ResourceID: &in.ID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": status, "serving_status": serving,
				"row_version": version, "server_id": *serverID, "server_status": serverStatus},
			AfterDigest: map[string]any{"status": "active", "serving_status": finalServing,
				"row_version": version + 1, "server_status": targetServer, "path": path},
		})
	})
	if err != nil {
		return nil, err
	}
	node, err := s.GetAdminNode(ctx, tenantID, in.ID)
	if err != nil {
		return nil, err
	}
	node.Warnings, err = s.activationWarnings(ctx, tenantID, node)
	if err != nil {
		return nil, err
	}
	res.Node = node
	return res, nil
}

// activateRefusal 说明为什么这个状态不能一步上线。
func activateRefusal(status string) string {
	switch status {
	case "draft", "provisioning", "bootstrapping":
		return "节点还没完成接入（" + status + "），等接入提交后再上线"
	case "provisioning_failed", "bootstrap_failed", "destroy_failed", "quarantined", "retired":
		return "节点处于 " + status + "，不能上线；需要重新接入或先处理这个状态"
	default:
		return "节点处于 " + status + "，不在接入尾段，请用启用或状态操作恢复服务"
	}
}

// activationWarnings 是上线成功但不会真正服务任何人的提示（与后台节点列表的
// delivery_note 同口径，R105）：没划进节点池，或池没绑到任何套餐版本。
func (s *Service) activationWarnings(ctx context.Context, tenantID string, n *AdminNode) ([]string, error) {
	if n.PoolID == nil {
		return []string{"未划入节点池，不服务任何用户"}, nil
	}
	var bound bool
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plan_node_pools
			WHERE tenant_id = $1 AND pool_id = $2::uuid)`, tenantID, *n.PoolID).Scan(&bound)
	}); err != nil {
		return nil, err
	}
	if !bound {
		return []string{"所在节点池没有绑定任何套餐，暂时不服务任何用户"}, nil
	}
	return nil, nil
}
