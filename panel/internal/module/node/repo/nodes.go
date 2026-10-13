package repo

// 本文件归 p5-inventory：节点的读写与写库错误的翻译。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

var (
	// ErrNodeNameTaken 表示节点名称已被未删除的节点占用（nodes_name_live_key）。
	ErrNodeNameTaken = errors.New("node: 节点名称已存在")
	// ErrListenClaimTaken 表示同一服务器上未退役的节点已占用这个 (端口, L4)（nodes_listen_claim_key）。
	ErrListenClaimTaken = errors.New("node: 同机端口已被占用")
	// ErrServerNameTaken 表示服务器名称已被未删除的服务器占用（servers_name_live_key）。
	ErrServerNameTaken = errors.New("node: 服务器名称已存在")
	// ErrInventoryCheck 表示写入不满足 CHECK 约束（23514）。
	ErrInventoryCheck = errors.New("node: 数据不满足库约束")
)

// inventoryErr 把写服务器、节点时的约束错误译成本包哨兵。GORM 的错误翻译用 %w 包着原错误，
// 约束名仍能取到：端口门禁的唯一索引与重名分开报。
func inventoryErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		switch pgErr.ConstraintName {
		case "nodes_listen_claim_key":
			return ErrListenClaimTaken
		case "servers_name_live_key":
			return ErrServerNameTaken
		}
		return ErrNodeNameTaken
	case "23514":
		return fmt.Errorf("%w: %v", ErrInventoryCheck, err)
	}
	return err
}

// nodeColumns 是读 nodes 一行的列清单（别名 n）；jsonb 以文本取出。
const nodeColumns = `n.id, n.node_no, n.server_id, n.pool_id, n.name, n.display_name, n.country_code,
 n.serving_status, n.node_type, n.server_host, n.server_port, n.listen_l4, n.kernel,
 n.protocol_config::text AS protocol_config, n.protocol_schema_version, n.config_validated_at,
 n.traffic_rate, n.sort_order, n.row_version,
 n.runtime_status, n.runtime_reason, n.runtime_reason_node_id, n.runtime_state_at,
 n.applied_config_sha256, n.applied_config_at,
 n.apply_failed_sha256, n.apply_failed_reason, n.apply_failed_detail, n.apply_failed_at,
 n.retired_at, n.deleted_at, n.created_at, n.updated_at`

// forUpdateSuffix 返回读一行时追加的锁子句。
func forUpdateSuffix(forUpdate bool) string {
	if forUpdate {
		return " FOR UPDATE"
	}
	return ""
}

// NodeByID 读一个未删除的节点；forUpdate 时加行锁。
func (r *Repo) NodeByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Node, bool, error) {
	var n model.Node
	err := r.d.Conn(ctx).Raw(
		"SELECT "+nodeColumns+" FROM nodes n WHERE n.id = ? AND n.deleted_at IS NULL"+forUpdateSuffix(forUpdate), id,
	).Scan(&n).Error
	if err != nil {
		return model.Node{}, false, err
	}
	if n.ID == uuid.Nil {
		return model.Node{}, false, nil
	}
	return n, true, nil
}

// NodesByIDsForUpdate 按 id 顺序锁住一批未删除的节点（批量改状态、排序用）；缺失的不在结果里。
func (r *Repo) NodesByIDsForUpdate(ctx context.Context, ids []uuid.UUID) ([]model.Node, error) {
	if len(ids) == 0 {
		return []model.Node{}, nil
	}
	var out []model.Node
	err := r.d.Conn(ctx).Raw(
		"SELECT "+nodeColumns+" FROM nodes n WHERE n.deleted_at IS NULL AND n.id IN ? ORDER BY n.id FOR UPDATE", ids,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.Node{}
	}
	return out, nil
}

// nodeInserted 是 INSERT ... RETURNING 回填的列。
type nodeInserted struct {
	ID        uuid.UUID `gorm:"column:id"`
	NodeNo    int64     `gorm:"column:node_no"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// InsertNode 插入一个节点，回填 ID、NodeNo、CreatedAt、UpdatedAt。
func (r *Repo) InsertNode(ctx context.Context, n *model.Node) error {
	var got nodeInserted
	err := r.d.Conn(ctx).Raw(`INSERT INTO nodes
		(server_id, pool_id, name, display_name, country_code, serving_status, node_type, server_host,
		 server_port, listen_l4, kernel, protocol_config, protocol_schema_version, config_validated_at,
		 traffic_rate, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS jsonb), ?, ?, ?, ?)
		RETURNING id, node_no, created_at, updated_at`,
		n.ServerID, n.PoolID, n.Name, n.DisplayName, n.CountryCode, n.ServingStatus, n.NodeType, n.ServerHost,
		n.ServerPort, n.ListenL4, n.Kernel, n.ProtocolConfig, n.ProtocolSchemaVersion, n.ConfigValidatedAt,
		n.TrafficRate, n.SortOrder,
	).Scan(&got).Error
	if err != nil {
		return inventoryErr(err)
	}
	n.ID, n.NodeNo, n.CreatedAt, n.UpdatedAt = got.ID, got.NodeNo, got.CreatedAt, got.UpdatedAt
	n.RowVersion = 1
	return nil
}

// UpdateNodeSpec 整体改写节点的可编辑列并把 row_version 加一；只改 row_version = expected 的未删除行。
func (r *Repo) UpdateNodeSpec(ctx context.Context, id uuid.UUID, expected int64, n *model.Node) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET
		name = ?, display_name = ?, country_code = ?, pool_id = ?, node_type = ?, server_host = ?,
		server_port = ?, listen_l4 = ?, kernel = ?, protocol_config = CAST(? AS jsonb),
		protocol_schema_version = ?, config_validated_at = ?, traffic_rate = ?,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL`,
		n.Name, n.DisplayName, n.CountryCode, n.PoolID, n.NodeType, n.ServerHost,
		n.ServerPort, n.ListenL4, n.Kernel, n.ProtocolConfig,
		n.ProtocolSchemaVersion, n.ConfigValidatedAt, n.TrafficRate,
		id, expected,
	)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// MoveNodeServer 把节点挂到另一台服务器。
func (r *Repo) MoveNodeServer(ctx context.Context, id uuid.UUID, expected int64, serverID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET server_id = ?, row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL`, serverID, id, expected)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SetNodeSortOrder 改节点的排序键。
func (r *Repo) SetNodeSortOrder(ctx context.Context, id uuid.UUID, expected int64, sortOrder int) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET sort_order = ?, row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL`, sortOrder, id, expected)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// SetNodeServing 改服务状态：只改 serving_status = from 且 row_version = expected 的行，0 行即并发冲突。
// 转 retired 时记 retired_at（库里 nodes_retired_shape 要求两者一致）。
func (r *Repo) SetNodeServing(ctx context.Context, id uuid.UUID, from string, expected int64, to string) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET serving_status = ?,
		retired_at = CASE WHEN ? = 'retired' THEN now() ELSE NULL END,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND serving_status = ? AND row_version = ? AND deleted_at IS NULL`,
		to, to, id, from, expected)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// nodeVersionRow 是 RETURNING row_version 的一行。
type nodeVersionRow struct {
	RowVersion int64 `gorm:"column:row_version"`
}

// BumpNodeVersion 把未退役、未删除节点的 row_version 加一（强制重新下发），返回新版本；没有这一行时 ok 为 false。
func (r *Repo) BumpNodeVersion(ctx context.Context, id uuid.UUID) (int64, bool, error) {
	var rows []nodeVersionRow
	err := r.d.Conn(ctx).Raw(`UPDATE nodes SET row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND deleted_at IS NULL AND serving_status <> 'retired'
		RETURNING row_version`, id).Scan(&rows).Error
	if err != nil {
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	return rows[0].RowVersion, true, nil
}

// SoftDeleteNode 软删一个已退役的节点（nodes_deleted_shape 要求删掉的必须是 retired）。
func (r *Repo) SoftDeleteNode(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET deleted_at = now(), row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL AND serving_status = 'retired'`, id, expected)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// nodeNameRow 是只取名字的一行。
type nodeNameRow struct {
	Name string `gorm:"column:name"`
}

// NodePortHolder 返回同服务器上占着 (port, l4) 的未退役节点的名字；exclude 非 nil 时排除它自己。
func (r *Repo) NodePortHolder(ctx context.Context, serverID uuid.UUID, port int, l4 string, exclude *uuid.UUID) (string, bool, error) {
	q := `SELECT n.name FROM nodes n
		WHERE n.server_id = ? AND n.server_port = ? AND n.listen_l4 = ?
		  AND n.serving_status <> 'retired' AND n.deleted_at IS NULL`
	args := []any{serverID, port, l4}
	if exclude != nil {
		q += " AND n.id <> ?"
		args = append(args, *exclude)
	}
	q += " ORDER BY n.created_at, n.id LIMIT 1"
	var rows []nodeNameRow
	if err := r.d.Conn(ctx).Raw(q, args...).Scan(&rows).Error; err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Name, true, nil
}

// nodeCountRow 是只取计数的一行。
type nodeCountRow struct {
	N int `gorm:"column:n"`
}

// CountLiveNodesOnServer 数服务器上未退役、未删除的节点（容量门槛）。
func (r *Repo) CountLiveNodesOnServer(ctx context.Context, serverID uuid.UUID) (int, error) {
	var c nodeCountRow
	err := r.d.Conn(ctx).Raw(`SELECT count(*)::int AS n FROM nodes
		WHERE server_id = ? AND deleted_at IS NULL AND serving_status <> 'retired'`, serverID).Scan(&c).Error
	return c.N, err
}

// ListServerNodes 列出服务器名下未删除的节点（含已退役），按 sort_order, name, id。
func (r *Repo) ListServerNodes(ctx context.Context, serverID uuid.UUID) ([]model.Node, error) {
	var out []model.Node
	err := r.d.Conn(ctx).Raw("SELECT "+nodeColumns+` FROM nodes n
		WHERE n.server_id = ? AND n.deleted_at IS NULL
		ORDER BY n.sort_order, n.name, n.id`, serverID).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.Node{}
	}
	return out, nil
}

// nodePoolStatusRow 是只取池状态的一行。
type nodePoolStatusRow struct {
	Status string `gorm:"column:status"`
}

// LockPoolForNode 以 FOR SHARE 锁住节点要划入的池并返回它的状态（建、改、复制节点时用）。
func (r *Repo) LockPoolForNode(ctx context.Context, poolID uuid.UUID) (string, bool, error) {
	var rows []nodePoolStatusRow
	err := r.d.Conn(ctx).Raw(`SELECT status FROM node_pools WHERE id = ? FOR SHARE`, poolID).Scan(&rows).Error
	if err != nil {
		return "", false, err
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return rows[0].Status, true, nil
}
