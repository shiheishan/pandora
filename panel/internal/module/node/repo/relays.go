package repo

// 中转节点（node_relays 表）的数据访问层。
// 中转只出现在订阅里，由站长自建转发；不占同机端口、不进配置文档与名单、没有服务状态机。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

var (
	// ErrRelayCheck 是中转节点数据不满足库 CHECK 约束（23514）。
	ErrRelayCheck = errors.New("node: 中转数据不满足库约束")
)

// relayErrs 把写 node_relays 表时的约束错误译成本包哨兵。
var relayErrs = db.ConstraintErrors{
	ByCode: map[string]error{db.CodeCheck: ErrRelayCheck},
}

// relayErr 把写 node_relays 时的约束错误译成本包哨兵。
func relayErr(err error) error { return relayErrs.Map(err) }

// relayColumns 是读 node_relays 一行的列清单。
const relayColumns = "id, parent_node_id, name, server_host, server_port, sni, host_header, enabled, sort_order, row_version, created_at, updated_at"

// RelaysByParent 列出一个父节点名下的全部中转节点（含停用的），按 sort_order, id 排序。
func (r *Repo) RelaysByParent(ctx context.Context, parentID uuid.UUID) ([]model.NodeRelay, error) {
	var out []model.NodeRelay
	err := r.d.Conn(ctx).Raw(
		"SELECT "+relayColumns+" FROM node_relays WHERE parent_node_id = ? ORDER BY sort_order, id",
		parentID,
	).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.NodeRelay{}
	}
	return out, nil
}

type relaySingleCountRow struct {
	Count int `gorm:"column:count"`
}

// CountRelays 统计单个父节点名下的中转条数（含停用的）。
func (r *Repo) CountRelays(ctx context.Context, parentID uuid.UUID) (int, error) {
	var row relaySingleCountRow
	err := r.d.Conn(ctx).Raw(
		"SELECT count(*)::int AS count FROM node_relays WHERE parent_node_id = ?",
		parentID,
	).Scan(&row).Error
	return row.Count, err
}

// RelayByID 根据 ID 查找单个中转；forUpdate 为 true 时加行锁。
func (r *Repo) RelayByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.NodeRelay, bool, error) {
	var row model.NodeRelay
	err := r.d.Conn(ctx).Raw(
		"SELECT "+relayColumns+" FROM node_relays WHERE id = ?"+forUpdateSuffix(forUpdate),
		id,
	).Scan(&row).Error
	if err != nil {
		return model.NodeRelay{}, false, err
	}
	if row.ID == uuid.Nil {
		return model.NodeRelay{}, false, nil
	}
	return row, true, nil
}

type relayInserted struct {
	ID         uuid.UUID `gorm:"column:id"`
	RowVersion int64     `gorm:"column:row_version"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

// InsertRelay 插入一条中转节点，回填 ID、RowVersion、CreatedAt、UpdatedAt。
func (r *Repo) InsertRelay(ctx context.Context, n *model.NodeRelay) error {
	var got relayInserted
	err := r.d.Conn(ctx).Raw(`INSERT INTO node_relays
		(parent_node_id, name, server_host, server_port, sni, host_header, enabled, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, row_version, created_at, updated_at`,
		n.ParentNodeID, n.Name, n.ServerHost, n.ServerPort, n.SNI, n.HostHeader, n.Enabled, n.SortOrder,
	).Scan(&got).Error
	if err != nil {
		return relayErr(err)
	}
	n.ID = got.ID
	n.RowVersion = got.RowVersion
	n.CreatedAt = got.CreatedAt
	n.UpdatedAt = got.UpdatedAt
	return nil
}

// UpdateRelay 修改中转的可变字段并推进 row_version，返回是否命中一行。
func (r *Repo) UpdateRelay(ctx context.Context, id uuid.UUID, expected int64, n *model.NodeRelay) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE node_relays SET
		name = ?, server_host = ?, server_port = ?, sni = ?, host_header = ?,
		enabled = ?, sort_order = ?,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ?`,
		n.Name, n.ServerHost, n.ServerPort, n.SNI, n.HostHeader,
		n.Enabled, n.SortOrder,
		id, expected,
	)
	if res.Error != nil {
		return false, relayErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// DeleteRelay 按 id 与 row_version 删除中转，返回是否命中一行。
func (r *Repo) DeleteRelay(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`DELETE FROM node_relays WHERE id = ? AND row_version = ?`, id, expected)
	if res.Error != nil {
		return false, relayErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}
