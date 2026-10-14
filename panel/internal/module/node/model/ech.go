package model

import (
	"time"

	"github.com/google/uuid"
)

// NodeECHKey 是 node_ech_keys 表的一行（迁移 00045_xb_node.sql）。SupersededAt 为空的是节点的当前密钥（每节点至多一把）；
// ConfigList 是公开的 ECHConfigList，PrivateKeySealed 是 X25519 私钥经 Sealer 封存（AAD "node_ech_key:<节点 id>"）。
type NodeECHKey struct {
	ID               uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	NodeID           uuid.UUID  `gorm:"column:node_id;type:uuid"`
	PublicName       string     `gorm:"column:public_name"`
	ConfigList       []byte     `gorm:"column:config_list"`
	PrivateKeySealed []byte     `gorm:"column:private_key_sealed"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	SupersededAt     *time.Time `gorm:"column:superseded_at"`
}

// TableName 固定表名。
func (NodeECHKey) TableName() string { return "node_ech_keys" }
