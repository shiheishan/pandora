// 节点生效配置文档组装用的批量读。
//
// 调用方按行的 node_id / group_id 分到各层；三条读各自取快照，读到成员快照里没有的组的行要丢掉（不能落进别的层）。
package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// ConfigNodeRow 是一台服务器与它上面一个该跑的节点（ServedOnServerCond）的协议列。
// 服务器存在、未删除但没有该跑的节点时只有一行，NodeID 为 nil。
type ConfigNodeRow struct {
	ServerID       uuid.UUID  `gorm:"column:server_id"`
	NodeID         *uuid.UUID `gorm:"column:node_id"`
	NodeType       *string    `gorm:"column:node_type"`
	ServerPort     *int       `gorm:"column:server_port"`
	ProtocolConfig *string    `gorm:"column:protocol_config"`
}

// ConfigNodes 批量读取服务器及其上该跑的节点协议信息。
func (r *Repo) ConfigNodes(ctx context.Context, serverIDs []uuid.UUID) ([]ConfigNodeRow, error) {
	if len(serverIDs) == 0 {
		return []ConfigNodeRow{}, nil
	}
	var rows []ConfigNodeRow
	err := r.d.Conn(ctx).Raw(`
		SELECT s.id AS server_id, n.id AS node_id, n.node_type, n.server_port,
		       n.protocol_config::text AS protocol_config
		  FROM servers s
		  LEFT JOIN nodes n ON n.server_id = s.id AND (`+ServedOnServerCond+`)
		 WHERE s.id IN ? AND s.deleted_at IS NULL
		 ORDER BY s.id, n.id`, serverIDs).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []ConfigNodeRow{}, nil
	}
	return rows, nil
}

// VisibleOutbounds 读取一批节点能看见的全部出站（全局、节点私有、所在组）。
func (r *Repo) VisibleOutbounds(ctx context.Context, nodeIDs []uuid.UUID) ([]model.NodeOutbound, error) {
	var outs []model.NodeOutbound
	if len(nodeIDs) == 0 {
		err := r.d.Conn(ctx).Raw(`
			SELECT id, node_id, group_id, tag, type, settings::text AS settings, sort_order, created_at
			  FROM node_outbounds
			 WHERE node_id IS NULL AND group_id IS NULL
			 ORDER BY sort_order, tag`).Scan(&outs).Error
		if err != nil {
			return nil, err
		}
	} else {
		err := r.d.Conn(ctx).Raw(`
			SELECT id, node_id, group_id, tag, type, settings::text AS settings, sort_order, created_at
			  FROM node_outbounds
			 WHERE (node_id IS NULL AND group_id IS NULL)
			    OR node_id IN ?
			    OR group_id IN (SELECT m.group_id FROM route_group_members m WHERE m.node_id IN ?)
			 ORDER BY sort_order, tag`, nodeIDs, nodeIDs).Scan(&outs).Error
		if err != nil {
			return nil, err
		}
	}
	if outs == nil {
		return []model.NodeOutbound{}, nil
	}
	return outs, nil
}

// VisibleEnabledRoutes 读取一批节点能看见的全部已启用规则（全局、节点私有、所在组）。
func (r *Repo) VisibleEnabledRoutes(ctx context.Context, nodeIDs []uuid.UUID) ([]model.NodeRoute, error) {
	var routes []model.NodeRoute
	if len(nodeIDs) == 0 {
		err := r.d.Conn(ctx).Raw(`
			SELECT id, node_id, group_id, priority, sort_order, matcher::text AS matcher,
			       outbound_tag, enabled, note, created_at
			  FROM node_routes
			 WHERE enabled AND node_id IS NULL AND group_id IS NULL
			 ORDER BY priority, sort_order`).Scan(&routes).Error
		if err != nil {
			return nil, err
		}
	} else {
		err := r.d.Conn(ctx).Raw(`
			SELECT id, node_id, group_id, priority, sort_order, matcher::text AS matcher,
			       outbound_tag, enabled, note, created_at
			  FROM node_routes
			 WHERE enabled
			   AND ((node_id IS NULL AND group_id IS NULL)
			        OR node_id IN ?
			        OR group_id IN (SELECT m.group_id FROM route_group_members m WHERE m.node_id IN ?))
			 ORDER BY priority, sort_order`, nodeIDs, nodeIDs).Scan(&routes).Error
		if err != nil {
			return nil, err
		}
	}
	if routes == nil {
		return []model.NodeRoute{}, nil
	}
	return routes, nil
}
