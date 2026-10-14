package repo

// ECH 密钥（node_ech_keys 表）的数据访问。

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// ECHKeysOfNode 返回节点的全部 ECH 密钥行，当前钥在前。
func (r *Repo) ECHKeysOfNode(ctx context.Context, nodeID uuid.UUID) ([]model.NodeECHKey, error) {
	var out []model.NodeECHKey
	err := r.d.Conn(ctx).Raw(`
		SELECT id, node_id, public_name, config_list, private_key_sealed, created_at, superseded_at
		  FROM node_ech_keys
		 WHERE node_id = ?
		 ORDER BY (superseded_at IS NULL) DESC, created_at DESC`, nodeID).Scan(&out).Error
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []model.NodeECHKey{}
	}
	return out, nil
}

type echKeyRow struct {
	NodeID           uuid.UUID  `gorm:"column:node_id"`
	PublicName       string     `gorm:"column:public_name"`
	ConfigList       []byte     `gorm:"column:config_list"`
	PrivateKeySealed []byte     `gorm:"column:private_key_sealed"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	SupersededAt     *time.Time `gorm:"column:superseded_at"`
}

// ECHKeysByNodes 批量取仍在下发窗口内的密钥（当前钥或 superseded_at 晚于 retiredAfter）。
func (r *Repo) ECHKeysByNodes(ctx context.Context, nodeIDs []uuid.UUID, retiredAfter time.Time) ([]model.NodeECHKey, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	var rows []echKeyRow
	err := r.d.Conn(ctx).Raw(`
		SELECT node_id, public_name, config_list, private_key_sealed, created_at, superseded_at
		  FROM node_ech_keys
		 WHERE node_id IN ?
		   AND (superseded_at IS NULL OR superseded_at > ?)
		 ORDER BY node_id, (superseded_at IS NULL) DESC, created_at DESC`, nodeIDs, retiredAfter).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]model.NodeECHKey, 0, len(rows))
	for _, row := range rows {
		out = append(out, model.NodeECHKey{
			NodeID:           row.NodeID,
			PublicName:       row.PublicName,
			ConfigList:       row.ConfigList,
			PrivateKeySealed: row.PrivateKeySealed,
			CreatedAt:        row.CreatedAt,
			SupersededAt:     row.SupersededAt,
		})
	}
	return out, nil
}

// SupersedeCurrentECHKey 把节点的当前钥标为已轮换；返回是否改到一行。
func (r *Repo) SupersedeCurrentECHKey(ctx context.Context, nodeID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`
		UPDATE node_ech_keys SET superseded_at = now()
		 WHERE node_id = ? AND superseded_at IS NULL`, nodeID)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

type echKeyInserted struct {
	ID        uuid.UUID `gorm:"column:id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// InsertECHKey 插入一行 ECH 密钥，回填 id、created_at。
func (r *Repo) InsertECHKey(ctx context.Context, k *model.NodeECHKey) error {
	var got echKeyInserted
	err := r.d.Conn(ctx).Raw(`
		INSERT INTO node_ech_keys (node_id, public_name, config_list, private_key_sealed)
		VALUES (?, ?, ?, ?)
		RETURNING id, created_at`,
		k.NodeID, k.PublicName, k.ConfigList, k.PrivateKeySealed,
	).Scan(&got).Error
	if err != nil {
		return err
	}
	k.ID = got.ID
	k.CreatedAt = got.CreatedAt
	return nil
}

// DeleteECHKeysOfNode 删除节点上的全部 ECH 密钥（含过渡期内旧钥）。
func (r *Repo) DeleteECHKeysOfNode(ctx context.Context, nodeID uuid.UUID) (int64, error) {
	res := r.d.Conn(ctx).Exec(`DELETE FROM node_ech_keys WHERE node_id = ?`, nodeID)
	return res.RowsAffected, res.Error
}

// PurgeRetiredECHKeys 删除 superseded_at 早于 before 的旧钥，返回删除条数。
func (r *Repo) PurgeRetiredECHKeys(ctx context.Context, before time.Time) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM node_ech_keys
		 WHERE superseded_at IS NOT NULL AND superseded_at < ?`, before)
	return res.RowsAffected, res.Error
}
