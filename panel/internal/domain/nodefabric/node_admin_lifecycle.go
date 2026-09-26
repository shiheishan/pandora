// [INPUT]: 依赖 node_admin.go 的 nodeVersionConflict 与输入类型，依赖 config_publish.go 的 lockLegacyConfigRelease，依赖 platform 的 audit/db/httpx
// [OUTPUT]: 对外提供 DeleteNodeInput、ValidServingTransition，Service 的 BatchAdminNodeLifecycle、DeleteNode
// [POS]: domain/nodefabric 后台节点的服务状态与删除：从 node_admin.go 拆出。服务状态迁移表只在 Go 内强制；批量改状态先取发布锁再锁节点行，退役同事务清 desired_config_version 并吊销有效身份；删除有三道「删了会立刻出事」的守卫
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var servingTransitions = map[string]map[string]bool{
	"draft":    {"active": true, "disabled": true, "retired": true},
	"active":   {"draining": true, "disabled": true},
	"draining": {"active": true, "disabled": true, "retired": true},
	"disabled": {"draft": true, "active": true, "retired": true},
	"retired":  {},
}

func ValidServingTransition(from, to string) bool { return servingTransitions[from][to] }

type lockedLifecycleNode struct {
	item    BatchNodeLifecycleItem
	current string
	version int64
	ready   bool
}

func (s *Service) BatchAdminNodeLifecycle(ctx context.Context, tenantID string, in BatchNodeLifecycleInput) error {
	if len(in.Items) == 0 || len(in.Items) > 100 {
		return httpx.Invalid(map[string]string{"items": "必须包含 1 到 100 个节点"})
	}
	if _, ok := servingTransitions[in.ServingStatus]; !ok {
		return httpx.Invalid(map[string]string{"serving_status": "不支持的状态"})
	}
	if len([]rune(strings.TrimSpace(in.Reason))) > 500 {
		return httpx.Invalid(map[string]string{"reason": "最多 500 个字符"})
	}
	sort.Slice(in.Items, func(i, j int) bool { return in.Items[i].ID < in.Items[j].ID })
	seen := map[string]bool{}
	for _, item := range in.Items {
		if validateAdminUUID("items.id", item.ID, true) != nil || item.RowVersion <= 0 || seen[item.ID] {
			return httpx.Invalid(map[string]string{"items": "节点 ID 必须唯一、格式正确且包含 row_version"})
		}
		seen[item.ID] = true
	}
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// Publish may update many Nodes in an implementation-defined row order.
		// Serialize every lifecycle batch with that release domain before taking
		// sorted Node locks so the two paths cannot form a lock-order cycle.
		if err := lockLegacyConfigRelease(ctx, tx, tenantID); err != nil {
			return err
		}
		locked := make([]lockedLifecycleNode, 0, len(in.Items))
		// Lock the full set in deterministic order before any mutation.
		for _, item := range in.Items {
			var current string
			var version int64
			var ready bool
			err := tx.QueryRow(ctx, `SELECT n.serving_status,n.row_version,
			 COALESCE(s.status='ready' AND s.deleted_at IS NULL
			 AND (s.control_node_id IS DISTINCT FROM n.id OR n.status='active')
			 AND n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535
			 AND `+StableProtocolReadySQL("n")+`,false)
				 FROM nodes n LEFT JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
				 WHERE n.tenant_id=$1 AND n.id=$2::uuid FOR UPDATE OF n`, tenantID, item.ID).Scan(&current, &version, &ready)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			if err != nil {
				return err
			}
			locked = append(locked, lockedLifecycleNode{item: item, current: current, version: version, ready: ready})
		}
		// Validate all members while every Node lock is held. No partial update is
		// attempted when one member is stale, unready or dependency-bound.
		for _, node := range locked {
			if node.version != node.item.RowVersion {
				return nodeVersionConflict(node.version)
			}
			if !ValidServingTransition(node.current, in.ServingStatus) {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "不允许的节点服务状态转换", Fields: map[string]string{"serving_status": node.current + " -> " + in.ServingStatus}}
			}
			if in.ServingStatus == "active" && !node.ready {
				return httpx.New(httpx.CodeConflict, "节点协议或服务器状态未满足启用条件")
			}
			if in.ServingStatus == "retired" {
				var deps int
				if err := tx.QueryRow(ctx, `SELECT
				 (SELECT count(*) FROM servers WHERE tenant_id=$1 AND control_node_id=$2::uuid)+
					 (SELECT count(*) FROM node_identities WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active')+
				 (SELECT count(*) FROM node_tasks WHERE tenant_id=$1 AND node_id=$2::uuid AND status IN ('pending','dispatched','running'))`,
					tenantID, node.item.ID).Scan(&deps); err != nil {
					return err
				}
				if deps > 0 {
					return httpx.New(httpx.CodeConflict, "节点仍有控制面、身份或任务依赖，不能退役")
				}
			}
		}
		for _, node := range locked {
			ct, err := tx.Exec(ctx, `UPDATE nodes SET serving_status=$4,
				desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END,
				row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid
			 AND row_version=$3`, tenantID, node.item.ID, node.item.RowVersion, in.ServingStatus)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return nodeVersionConflict(node.version)
			}
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.status_batch", ResourceType: "node_set", APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"count": len(in.Items), "serving_status": in.ServingStatus, "reason": in.Reason}})
	})
}

// DeleteNodeInput 是删除一个逻辑节点的入参。
type DeleteNodeInput struct {
	ID         string
	RowVersion int64
	ActorID    string
	Reason     string
}

// DeleteNode 销毁一个逻辑节点。
//
// 是转终态而不是 DELETE。原本写的是硬删，跑起来才发现数据库不允许：
// node_config_applications 是追加写表（DATA-003），nodes 上的
// ON DELETE CASCADE 撞到它的 deny_mutation 触发器就整个事务失败。
// 那张表记的是「什么时候给这个节点下发过哪一版配置」，属于证据链 ——
// 数据库这条约束是对的，该改的是这里。
//
// 所以走 status='destroyed'：这个终态本来就在 schema 的取值里，
// 服务发现、订阅渲染、节点列表都已经把它排除在外，行为上等于删掉了。
//
// 名字要一并让出来。(tenant_id, name) 上有唯一索引，不改名的话
// 「删掉 hk-01 再建一个 hk-01」会撞唯一约束，而这是运维最自然的动作。
// 改成 name#destroyed-时间戳，既腾出名字，又让人在库里还认得出它是谁。
//
// 三道守卫，都是「删了会立刻出事」的情形：
//   - 还在服的节点：用户订阅里正带着它，删了就是一批人当场断线
//   - 有活跃运行实例：节点端还跑着它的入站，面板这边删了两边就对不上
//   - 服务器的控制节点：servers.control_node_id 是 NO ACTION，
//     删了数据库会直接报外键错，与其让用户看见一段 23503，不如说人话
func (s *Service) DeleteNode(ctx context.Context, tenantID string, in DeleteNodeInput) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID},
		func(tx pgx.Tx) error {
			var name, status, servingStatus string
			var currentVersion int64
			var isControl bool
			var liveInstances int
			err := tx.QueryRow(ctx, `
				SELECT n.name, n.status, n.serving_status, n.row_version,
				       -- 只有「服务器还活着」时它才算不可删的控制节点。
				       -- 服务器自己都退役了，就不存在「失去管理入口」这回事，
				       -- 而不排除这种情况会形成死锁：销毁节点要求它不是控制
				       -- 节点，删服务器又要求名下 0 个节点，一台服务器上唯一
				       -- 的那个节点于是永远清不掉。
				       EXISTS (SELECT 1 FROM servers s
				                WHERE s.tenant_id = n.tenant_id AND s.control_node_id = n.id
				                  AND s.deleted_at IS NULL
				                  AND s.status NOT IN ('retired', 'destroyed')),
				       (SELECT count(*) FROM runtime_instances i
				         WHERE i.tenant_id = n.tenant_id AND i.node_id = n.id
				           AND i.status = 'active')
				  FROM nodes n
				 WHERE n.tenant_id = $1 AND n.id = $2::uuid
				 FOR UPDATE`, tenantID, in.ID).
				Scan(&name, &status, &servingStatus, &currentVersion, &isControl, &liveInstances)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.New(httpx.CodeNotFound, "节点不存在")
			}
			if err != nil {
				return err
			}
			if in.RowVersion != 0 && in.RowVersion != currentVersion {
				return nodeVersionConflict(currentVersion)
			}
			if servingStatus == "active" || servingStatus == "draining" {
				return httpx.New(httpx.CodeValidationFailed,
					"节点还在服务中。请先把它下线（改为 retired），确认没有用户在用之后再删")
			}
			if isControl {
				return httpx.New(httpx.CodeValidationFailed,
					"这是一台在役服务器的控制节点，删它会让那台服务器失去管理入口。"+
						"请先把那台服务器退役")
			}
			if liveInstances > 0 {
				return httpx.New(httpx.CodeValidationFailed,
					"节点端还在运行这个节点的入站，等它停下来再删")
			}

			if status == "destroyed" {
				return httpx.New(httpx.CodeValidationFailed, "节点已经销毁过了")
			}

			// 销毁前校验生命周期状态：NODE-010 状态机只允许从
			// draft/provisioning_failed/bootstrap_failed/retired/destroy_failed
			// 直接进入 destroyed，其余状态（active/draining/standby 等）需先
			// 走合法路径退役。这里对允许直接销毁的状态直接执行，其余状态
			// 一律要求先退役，避免 UPDATE 触发状态机守卫返回数据库错误。
			directDestroy := map[string]bool{
				"draft":               true,
				"provisioning_failed": true,
				"bootstrap_failed":    true,
				"retired":             true,
				"destroy_failed":      true,
			}
			if !directDestroy[status] {
				return httpx.New(httpx.CodeValidationFailed,
					"节点当前状态不能直接销毁，请先将其退役（retired）后再删除")
			}

			// 名字后面缀上销毁时刻。用秒级时间戳而不是随机串：
			// 同一个名字被建了又销毁多次时，看一眼就知道先后。
			retiredName := fmt.Sprintf("%s#destroyed-%d", name, time.Now().Unix())
			if len(retiredName) > 120 {
				// name 上有长度约束，太长的名字截掉前面一段再拼
				retiredName = retiredName[len(retiredName)-120:]
			}

			ct, err := tx.Exec(ctx, `
				UPDATE nodes
				   SET status = 'destroyed', serving_status = 'retired',
				       destroyed_at = now(), entered_status_at = now(),
				       name = $4, row_version = row_version + 1, updated_at = now()
				 WHERE tenant_id = $1 AND id = $2::uuid AND row_version = $3`,
				tenantID, in.ID, currentVersion, retiredName)
			if err != nil {
				return err
			}
			if ct.RowsAffected() != 1 {
				return errors.New("node destroy transition lost")
			}

			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &in.ActorID,
				Action: "node.delete", ResourceType: "node", ResourceID: &in.ID,
				BeforeDigest: map[string]any{
					"name": name, "status": status,
					"serving_status": servingStatus, "row_version": currentVersion,
				},
				AfterDigest: map[string]any{
					"status": "destroyed", "renamed_to": retiredName,
					"reason": in.Reason,
				},
				APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			})
		})
}
