package nodefabric

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type RetireNodeInput struct {
	ID         string
	RowVersion int64
	Reason     string
	ActorID    string
}

// retirePaths 是各生命周期态到 retired 的合法路径（每一步都是 node_transitions 里的边）。
// 没列出的在途状态（provisioning、bootstrapping、attesting、installing、validating）
// 不能直接退役：接入还在跑，强行改状态会和节点端的回报打架。
var retirePaths = map[string][]string{
	"standby":        {"retired"},
	"quarantined":    {"retired"},
	"maintenance":    {"retired"},
	"draining":       {"retired"},
	"active":         {"draining", "retired"},
	"unhealthy":      {"draining", "retired"},
	"upgrade_failed": {"draining", "retired"},
	"canary":         {"standby", "retired"},
	// 这三种可以直接销毁：生命周期保持不动，只把服务状态置 retired
	"draft":               nil,
	"provisioning_failed": nil,
	"bootstrap_failed":    nil,
	// 已经在退役之后的终局路径上
	"retired":        nil,
	"destroy_failed": nil,
}

// controlsLiveServerSQL 是「节点仍是某台在役服务器的控制节点」的唯一口径：单个退役（RetireNode）、
// 批量改状态退役（status:batch）与删除（DeleteNode）共用。服务器已退役、已销毁或已删除，它的控制
// 节点就不再算依赖：接入时每个节点都会成为自己服务器的控制节点，不排除这种情况，节点永远退不掉。
// 服务器还在用时仍然拒绝，免得服务器失去管理入口。alias 是外层查询里 nodes 表的别名。
func controlsLiveServerSQL(alias string) string {
	return `EXISTS (SELECT 1 FROM servers s
	                WHERE s.tenant_id = ` + alias + `.tenant_id AND s.control_node_id = ` + alias + `.id
	                  AND s.deleted_at IS NULL AND s.status NOT IN ('retired','destroyed'))`
}

// errControlsLiveServer 是退役在役服务器的控制节点时的拒绝（单个与批量同一句话）。
func errControlsLiveServer() *httpx.Error {
	return httpx.New(httpx.CodeConflict, "这是一台在役服务器的控制节点，请先把那台服务器退役")
}

// RetireNode 在一个事务里把节点彻底退役。
//
// 与 status:batch 退役的区别：那条只改服务状态，遇到有效身份就 409，之后 DELETE 仍因
// 生命周期没退役而 422 ——「退役 → 删除」两步走不通。这里一步到位。控制节点的判定两条路径
// 同一口径（controlsLiveServerSQL）。
func (s *Service) RetireNode(ctx context.Context, tenantID string, in RetireNodeInput) (*AdminNode, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	if len([]rune(in.Reason)) > 500 {
		return nil, httpx.Invalid(map[string]string{"reason": "最多 500 个字符"})
	}
	if in.RowVersion <= 0 {
		return nil, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if validateAdminUUID("id", in.ID, true) != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// 与配置发布、批量改状态同一把锁：退役要清掉 desired_config_version，
		// 不能和一次正在进行的发布交错
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		var status, serving string
		var version int64
		var isControl bool
		err := tx.QueryRow(ctx, `
			SELECT n.status, n.serving_status, n.row_version, `+controlsLiveServerSQL("n")+`
			  FROM nodes n
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid AND n.status <> 'destroyed'
			 FOR UPDATE OF n`, tenantID, in.ID).Scan(&status, &serving, &version, &isControl)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if version != in.RowVersion {
			return nodeVersionConflict(version)
		}
		if serving == "retired" {
			return httpx.New(httpx.CodeConflict, "节点已经退役")
		}
		if isControl {
			return errControlsLiveServer()
		}
		path, ok := retirePaths[status]
		if !ok {
			return httpx.New(httpx.CodeConflict, "节点正在接入中（"+status+"），等接入结束再退役")
		}
		for _, next := range path {
			if _, err := tx.Exec(ctx, `
				UPDATE nodes SET status = $3, entered_status_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, in.ID, next); err != nil {
				if db.IsCheckViolation(err) {
					return NodeStatusRefusal(err)
				}
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE nodes SET serving_status = 'retired', desired_config_version = NULL,
			       row_version = row_version + 1
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, in.ID); err != nil {
			return err
		}
		revoked, err := tx.Exec(ctx, `
			UPDATE node_identities SET status = 'revoked', revoked_at = now(), revoked_reason = '节点已退役'
			 WHERE tenant_id = $1 AND node_id = $2::uuid AND status = 'active'`, tenantID, in.ID)
		if err != nil {
			return err
		}
		failed, err := tx.Exec(ctx, `
			UPDATE node_tasks SET status = 'failed', error_message = '节点已退役', completed_at = now()
			 WHERE tenant_id = $1 AND node_id = $2::uuid AND status IN ('pending','dispatched','running')`,
			tenantID, in.ID)
		if err != nil {
			return err
		}
		finalStatus := status
		if len(path) > 0 {
			finalStatus = path[len(path)-1]
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.retire", ResourceType: "node", ResourceID: &in.ID,
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"status": status, "serving_status": serving, "row_version": version},
			AfterDigest: map[string]any{"status": finalStatus, "serving_status": "retired",
				"identities_revoked": revoked.RowsAffected(), "tasks_failed": failed.RowsAffected(),
				"reason": in.Reason},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.GetAdminNode(ctx, tenantID, in.ID)
}
