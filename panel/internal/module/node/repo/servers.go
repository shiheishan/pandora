package repo

// 本文件归 p5-inventory：服务器的读写。

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// serverColumns 是读 servers 一行的列清单（别名 s）；inet 列用 host() 取纯地址。
const serverColumns = `s.id, s.name, s.status, s.status_reason, s.row_version, s.region,
 host(s.public_ipv4) AS public_ipv4, host(s.public_ipv6) AS public_ipv6, host(s.private_ipv4) AS private_ipv4,
 s.capacity_nodes, s.notes, s.hostname, s.os_name, s.architecture, s.agent_version, s.binary_sha256,
 s.cpu_cores, s.memory_mb, s.disk_gb, s.last_heartbeat_at, s.retired_at, s.deleted_at, s.created_at, s.updated_at`

// serverListLimit 是服务器列表一次最多返回的行数。
const serverListLimit = 500

// ServerNodeCount 是一台服务器名下节点的计数：Total 未删除的全部，Active 在役，Live 未退役。
type ServerNodeCount struct {
	Total  int
	Active int
	Live   int
}

// ServerByID 读一台未删除的服务器；forUpdate 时加行锁。
func (r *Repo) ServerByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Server, bool, error) {
	var s model.Server
	err := r.d.Conn(ctx).Raw(
		"SELECT "+serverColumns+" FROM servers s WHERE s.id = ? AND s.deleted_at IS NULL"+forUpdateSuffix(forUpdate), id,
	).Scan(&s).Error
	if err != nil {
		return model.Server{}, false, err
	}
	if s.ID == uuid.Nil {
		return model.Server{}, false, nil
	}
	return s, true, nil
}

// ListServers 列出未删除的服务器，按创建时间倒序，最多 serverListLimit 行；status、query 为空时不筛。
func (r *Repo) ListServers(ctx context.Context, status, query string) ([]model.Server, error) {
	q := "SELECT " + serverColumns + " FROM servers s WHERE s.deleted_at IS NULL"
	args := []any{}
	if status != "" {
		q += " AND s.status = ?"
		args = append(args, status)
	}
	if query != "" {
		q += " AND (strpos(lower(s.name), lower(?::text)) > 0 OR strpos(lower(coalesce(s.hostname, '')), lower(?::text)) > 0)"
		args = append(args, query, query)
	}
	q += " ORDER BY s.created_at DESC, s.id DESC LIMIT ?"
	args = append(args, serverListLimit)
	var out []model.Server
	if err := r.d.Conn(ctx).Raw(q, args...).Scan(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.Server{}
	}
	return out, nil
}

// serverNodeCountRow 是按服务器分组计数的一行。
type serverNodeCountRow struct {
	ServerID uuid.UUID `gorm:"column:server_id"`
	Total    int       `gorm:"column:total"`
	Active   int       `gorm:"column:active"`
	Live     int       `gorm:"column:live"`
}

// ServerNodeCounts 一条 SQL 数出这些服务器名下未删除节点的数量；没有节点的服务器不在结果里。
func (r *Repo) ServerNodeCounts(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID]ServerNodeCount, error) {
	out := make(map[uuid.UUID]ServerNodeCount, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	var rows []serverNodeCountRow
	err := r.d.Conn(ctx).Raw(`SELECT server_id,
		count(*)::int AS total,
		(count(*) FILTER (WHERE serving_status = 'active'))::int AS active,
		(count(*) FILTER (WHERE serving_status <> 'retired'))::int AS live
		FROM nodes WHERE deleted_at IS NULL AND server_id IN ? GROUP BY server_id`, serverIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ServerID] = ServerNodeCount{Total: row.Total, Active: row.Active, Live: row.Live}
	}
	return out, nil
}

// serverInserted 是 INSERT ... RETURNING 回填的列。
type serverInserted struct {
	ID         uuid.UUID `gorm:"column:id"`
	Status     string    `gorm:"column:status"`
	RowVersion int64     `gorm:"column:row_version"`
}

// InsertServer 插入一台草稿服务器，回填 ID、Status、RowVersion；时间戳由调用方需要时再读。
func (r *Repo) InsertServer(ctx context.Context, s *model.Server) error {
	var got serverInserted
	err := r.d.Conn(ctx).Raw(`INSERT INTO servers
		(name, status, region, hostname, public_ipv4, public_ipv6, private_ipv4, architecture, os_name, capacity_nodes, notes)
		VALUES (?, 'draft', ?, ?, CAST(? AS inet), CAST(? AS inet), CAST(? AS inet), ?, ?, ?, ?)
		RETURNING id, status, row_version`,
		s.Name, s.Region, s.Hostname, s.PublicIPv4, s.PublicIPv6, s.PrivateIPv4,
		s.Architecture, s.OSName, s.CapacityNodes, s.Notes,
	).Scan(&got).Error
	if err != nil {
		return inventoryErr(err)
	}
	s.ID, s.Status, s.RowVersion = got.ID, got.Status, got.RowVersion
	return nil
}

// UpdateServerProfile 整体改写服务器的可编辑列并把 row_version 加一；只改 row_version = expected 的未删除行。
func (r *Repo) UpdateServerProfile(ctx context.Context, id uuid.UUID, expected int64, s *model.Server) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE servers SET
		name = ?, region = ?, hostname = ?,
		public_ipv4 = CAST(? AS inet), public_ipv6 = CAST(? AS inet), private_ipv4 = CAST(? AS inet),
		architecture = ?, os_name = ?, capacity_nodes = ?, notes = ?,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL`,
		s.Name, s.Region, s.Hostname, s.PublicIPv4, s.PublicIPv6, s.PrivateIPv4,
		s.Architecture, s.OSName, s.CapacityNodes, s.Notes,
		id, expected,
	)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SetServerStatus 改服务器状态：只改 status = from 且 row_version = expected 的未删除行；转 retired 时记 retired_at。
func (r *Repo) SetServerStatus(ctx context.Context, id uuid.UUID, from string, expected int64, to string, reason *string) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE servers SET status = ?, status_reason = ?,
		retired_at = CASE WHEN ? = 'retired' THEN now() ELSE NULL END,
		row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND status = ? AND row_version = ? AND deleted_at IS NULL`,
		to, reason, to, id, from, expected)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// SoftDeleteServer 软删一台草稿或已退役的服务器（servers_deleted_shape），删后状态记为 retired。
func (r *Repo) SoftDeleteServer(ctx context.Context, id uuid.UUID, expected int64) (bool, error) {
	res := r.d.Conn(ctx).Exec(`UPDATE servers SET deleted_at = now(), status = 'retired',
		retired_at = coalesce(retired_at, now()), row_version = row_version + 1, updated_at = now()
		WHERE id = ? AND row_version = ? AND deleted_at IS NULL AND status IN ('draft', 'retired')`, id, expected)
	if res.Error != nil {
		return false, inventoryErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// serverExistsRow 是 EXISTS 查询的一行。
type serverExistsRow struct {
	Found bool `gorm:"column:found"`
}

// ServerHasActiveNode 报告服务器名下有没有未删除的在役节点（服务器转 ready 的前提）。
func (r *Repo) ServerHasActiveNode(ctx context.Context, serverID uuid.UUID) (bool, error) {
	var row serverExistsRow
	err := r.d.Conn(ctx).Raw(`SELECT EXISTS (SELECT 1 FROM nodes
		WHERE server_id = ? AND deleted_at IS NULL AND serving_status = 'active') AS found`, serverID).Scan(&row).Error
	return row.Found, err
}

// RetireServerNodes 删服务器时退役它名下未退役的节点，再把全部节点从这台服务器上摘下（级联静默）；
// 返回退役的行数与被摘下的未删除节点 id（给实时推送用）。
func (r *Repo) RetireServerNodes(ctx context.Context, serverID uuid.UUID) (retired int64, nodeIDs []uuid.UUID, err error) {
	res := r.d.Conn(ctx).Exec(`UPDATE nodes SET serving_status = 'retired', retired_at = now(),
		row_version = row_version + 1, updated_at = now()
		WHERE server_id = ? AND deleted_at IS NULL AND serving_status <> 'retired'`, serverID)
	if res.Error != nil {
		return 0, nil, inventoryErr(res.Error)
	}
	retired = res.RowsAffected
	var rows []struct {
		ID   uuid.UUID `gorm:"column:id"`
		Live bool      `gorm:"column:live"`
	}
	if err := r.d.Conn(ctx).Raw(`UPDATE nodes SET server_id = NULL, updated_at = now() WHERE server_id = ? RETURNING id, deleted_at IS NULL AS live`, serverID).Scan(&rows).Error; err != nil {
		return 0, nil, inventoryErr(err)
	}
	nodeIDs = []uuid.UUID{}
	for _, row := range rows {
		if row.Live {
			nodeIDs = append(nodeIDs, row.ID)
		}
	}
	return retired, nodeIDs, nil
}
