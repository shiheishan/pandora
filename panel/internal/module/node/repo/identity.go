// 服务器的接入身份（server_identities）与身份相关的服务器行读写。
package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// IdentityServerState 是签令牌、接入、续期、吊销前锁住的服务器行。
type IdentityServerState struct {
	ID      uuid.UUID `gorm:"column:id"`
	Status  string    `gorm:"column:status"`
	Deleted bool      `gorm:"column:deleted"`
}

// LockServerForIdentity 锁住服务器行；没有这行返回 nil, nil。同一台服务器的签令牌、接入、续期、吊销靠它串行。
func (r *Repo) LockServerForIdentity(ctx context.Context, serverID uuid.UUID) (*IdentityServerState, error) {
	var rows []IdentityServerState
	err := r.d.Conn(ctx).Raw(
		`SELECT id, status, deleted_at IS NOT NULL AS deleted FROM servers WHERE id = ? FOR UPDATE`, serverID,
	).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// NodeServerID 取未删除节点所在的服务器：节点不存在或已删除 found=false；节点没挂服务器 found=true、serverID=nil。
func (r *Repo) NodeServerID(ctx context.Context, nodeID uuid.UUID) (serverID *uuid.UUID, found bool, err error) {
	var rows []struct {
		ServerID *uuid.UUID `gorm:"column:server_id"`
	}
	err = r.d.Conn(ctx).Raw(`SELECT server_id FROM nodes WHERE id = ? AND deleted_at IS NULL`, nodeID).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, false, err
	}
	return rows[0].ServerID, true, nil
}

// firstIdentity 按条件取一份身份；没有返回 nil, nil。
func (r *Repo) firstIdentity(ctx context.Context, order string, query string, args ...any) (*model.ServerIdentity, error) {
	var rows []model.ServerIdentity
	q := r.d.Conn(ctx).Where(query, args...)
	if order != "" {
		q = q.Order(order)
	}
	if err := q.Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// ActiveIdentity 取这台服务器的有效身份；没有返回 nil, nil。
func (r *Repo) ActiveIdentity(ctx context.Context, serverID uuid.UUID) (*model.ServerIdentity, error) {
	return r.firstIdentity(ctx, "", "server_id = ? AND status = 'active'", serverID)
}

// LatestIdentity 取这台服务器最近一份曾经生效过的身份；没有返回 nil, nil。
// 先给 active；没有 active 时给最近一份曾经生效过的（被取代或吊销的）；pending 与从没转正就被取代/吊销的续期证书一律不算。
func (r *Repo) LatestIdentity(ctx context.Context, serverID uuid.UUID) (*model.ServerIdentity, error) {
	return r.firstIdentity(ctx,
		"(status = 'active') DESC, activated_at DESC, seq DESC",
		"server_id = ? AND activated_at IS NOT NULL", serverID)
}

// IdentityByEnrollToken 取用这张令牌签发的身份；没有返回 nil, nil。
func (r *Repo) IdentityByEnrollToken(ctx context.Context, tokenID uuid.UUID) (*model.ServerIdentity, error) {
	return r.firstIdentity(ctx, "", "enroll_token_id = ?", tokenID)
}

// NextIdentitySeq 返回这台服务器下一个 seq；调用方已锁住服务器行。
func (r *Repo) NextIdentitySeq(ctx context.Context, serverID uuid.UUID) (int32, error) {
	var row struct {
		N int32 `gorm:"column:n"`
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT coalesce(max(seq), 0) + 1 AS n FROM server_identities WHERE server_id = ?`, serverID,
	).Scan(&row).Error
	return row.N, err
}

// InsertIdentity 写入一份身份（调用方填好全部列）；active 且没给 ActivatedAt 时按签发时刻补上。写回 ID、CreatedAt。
func (r *Repo) InsertIdentity(ctx context.Context, id *model.ServerIdentity) error {
	if id.Status == "active" && id.ActivatedAt == nil {
		at := id.IssuedAt
		id.ActivatedAt = &at
	}
	return r.d.Conn(ctx).Create(id).Error
}

type identityIDRow struct {
	ID uuid.UUID `gorm:"column:id"`
}

// SupersedeIdentity 把这台服务器处于 status（active 或 pending）的那份身份置为 superseded；
// 返回被取代的身份 id 与是否有这一行（每种状态至多一行，部分唯一索引）。
func (r *Repo) SupersedeIdentity(ctx context.Context, serverID uuid.UUID, status string, at time.Time) (uuid.UUID, bool, error) {
	var rows []identityIDRow
	err := r.d.Conn(ctx).Raw(
		`UPDATE server_identities SET status = 'superseded', superseded_at = ?
		 WHERE server_id = ? AND status = ? RETURNING id`, at, serverID, status,
	).Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return uuid.Nil, false, err
	}
	return rows[0].ID, true, nil
}

// ActivatePendingIdentity 把一份待生效身份置为 active 并写入 activated_at；返回是否真的改了一行。调用方已锁住服务器行、已取代旧的 active。
func (r *Repo) ActivatePendingIdentity(ctx context.Context, id uuid.UUID, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE server_identities SET status = 'active', activated_at = ? WHERE id = ? AND status = 'pending'`,
		at, id)
	return res.RowsAffected == 1, res.Error
}

// PendingIdentityIssuedAt 取这台服务器待生效身份的签发时刻；没有返回 nil, nil。
func (r *Repo) PendingIdentityIssuedAt(ctx context.Context, serverID uuid.UUID) (*time.Time, error) {
	var rows []struct {
		IssuedAt time.Time `gorm:"column:issued_at"`
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT issued_at FROM server_identities WHERE server_id = ? AND status = 'pending'`, serverID,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	t := rows[0].IssuedAt
	return &t, nil
}

// RevokeIdentities 把这台服务器的有效与待生效身份都置为 revoked；返回被吊销的身份 id（active 在前），没有返回空。
func (r *Repo) RevokeIdentities(ctx context.Context, serverID uuid.UUID, reason string, at time.Time) ([]uuid.UUID, error) {
	var rows []identityIDRow
	err := r.d.Conn(ctx).Raw(
		`WITH x AS (
		   UPDATE server_identities SET status = 'revoked', revoked_at = ?, revoked_reason = ?
		   WHERE server_id = ? AND status IN ('active', 'pending') RETURNING id, seq
		 ) SELECT id FROM x ORDER BY seq`, at, reason, serverID,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

type identityOKRow struct {
	OK bool `gorm:"column:ok"`
}

// SessionIdentity 是建流校验查到的那份身份。
type SessionIdentity struct {
	ID     uuid.UUID `gorm:"column:id"`
	Status string    `gorm:"column:status"` // active 或 pending
}

// SessionIdentity 建流时的身份校验（一条 SQL）：这台服务器该序列号的身份须是 active 或 pending、未过期，
// 且服务器未删除、未退役。不符合返回 nil, nil。加 forUpdate 时锁住身份行（pending 转正前用）。
func (r *Repo) SessionIdentity(ctx context.Context, serverID uuid.UUID, serialHex string, now time.Time, forUpdate bool) (*SessionIdentity, error) {
	q := `SELECT i.id, i.status FROM server_identities i JOIN servers s ON s.id = i.server_id
		 WHERE i.server_id = ? AND i.cert_serial = ? AND i.status IN ('active', 'pending') AND i.expires_at > ?
		   AND s.deleted_at IS NULL AND s.status <> 'retired'`
	if forUpdate {
		q += ` FOR UPDATE OF i`
	}
	var rows []SessionIdentity
	if err := r.d.Conn(ctx).Raw(q, serverID, serialHex, now).Scan(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// HasActiveIdentity 这台服务器有没有未过期的有效身份。
func (r *Repo) HasActiveIdentity(ctx context.Context, serverID uuid.UUID, now time.Time) (bool, error) {
	var row identityOKRow
	err := r.d.Conn(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM server_identities
		 WHERE server_id = ? AND status = 'active' AND expires_at > ?) AS ok`, serverID, now,
	).Scan(&row).Error
	return row.OK, err
}

// ActiveIdentitySeqs 批量取这些服务器当前 active 且未过期身份的 seq（一条 SQL，与 HasActiveIdentity 同口径）；没有的服务器不在结果里。
func (r *Repo) ActiveIdentitySeqs(ctx context.Context, serverIDs []uuid.UUID, now time.Time) (map[uuid.UUID]int32, error) {
	out := make(map[uuid.UUID]int32, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ServerID uuid.UUID `gorm:"column:server_id"`
		Seq      int32     `gorm:"column:seq"`
	}
	err := r.d.Conn(ctx).Raw(
		`SELECT server_id, seq FROM server_identities WHERE server_id IN ? AND status = 'active' AND expires_at > ?`, serverIDs, now,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ServerID] = row.Seq
	}
	return out, nil
}

// ServerHostInfo 是节点自报的主机信息；nil 表示未知（写成 NULL）。
type ServerHostInfo struct {
	Hostname, OSName, Architecture, AgentVersion *string
	CPUCores, MemoryMB, DiskGB                   *int32
}

// UpdateServerHostInfo 写服务器的主机信息列，与现值不同才写（同时 updated_at = now()）；不动 row_version
// （节点自报的列不该让后台编辑撞版本冲突）。
func (r *Repo) UpdateServerHostInfo(ctx context.Context, serverID uuid.UUID, h ServerHostInfo) error {
	vals := []any{h.Hostname, h.OSName, h.Architecture, h.AgentVersion, h.CPUCores, h.MemoryMB, h.DiskGB}
	args := append(append(append([]any{}, vals...), serverID), vals...)
	return r.d.Conn(ctx).Exec(
		`UPDATE servers SET hostname = ?, os_name = ?, architecture = ?, agent_version = ?,
		   cpu_cores = ?, memory_mb = ?, disk_gb = ?, updated_at = now()
		 WHERE id = ? AND (hostname, os_name, architecture, agent_version, cpu_cores, memory_mb, disk_gb)
		   IS DISTINCT FROM (?::text, ?::text, ?::text, ?::text, ?::integer, ?::integer, ?::integer)`,
		args...,
	).Error
}

// FillServerRegionIfEmpty 在服务器 region 为空时写入地区文本，管理员填过的不覆盖。
func (r *Repo) FillServerRegionIfEmpty(ctx context.Context, serverID uuid.UUID, region string) error {
	return r.d.Conn(ctx).Exec(
		`UPDATE servers SET region = ? WHERE id = ? AND (region IS NULL OR btrim(region) = '')`,
		region, serverID,
	).Error
}

