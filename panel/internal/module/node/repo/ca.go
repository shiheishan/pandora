// 面板节点 CA（node_ca 单行）。
package repo

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// LoadCA 取 node_ca 那一行；没有返回 nil, nil。
func (r *Repo) LoadCA(ctx context.Context) (*model.NodeCA, error) {
	var ca model.NodeCA
	err := r.d.Conn(ctx).Limit(1).Find(&ca).Error
	if err != nil {
		return nil, err
	}
	if ca.ID == 0 {
		return nil, nil
	}
	return &ca, nil
}

// InsertCAIfAbsent 写入 CA 行；已有行时什么也不做（ON CONFLICT (id) DO NOTHING）。
func (r *Repo) InsertCAIfAbsent(ctx context.Context, ca *model.NodeCA) error {
	err := r.d.Conn(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		DoNothing: true,
	}).Create(ca).Error
	if err != nil {
		return err
	}
	return nil
}
