// Package repo 是 node 模块唯一使用 GORM 的数据访问层。
//
// 各块按主题分文件（servers、nodes、pools…）；本文件只放 Repo 与跨主题共用的口径。
package repo

import (
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// Repo 是 node 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// DeliverableNodeCond 是「节点自己能不能发给用户」的唯一口径（与用户、套餐都无关的那一半）：
// 节点未删除且在役（在役的形状由 nodes_active_shape 钉住：挂在服务器上、协议配好）、本周期流量没超限（traffic_exceeded_at，迁移 00045），
// 服务器未删除、就绪、见过心跳。心跳超时不排除（agent 挂了而代理还在跑很常见），降级在 Go 侧做。
//
// 调用方的 SQL 里节点表别名必须是 n、服务器表别名必须是 s（JOIN servers s ON s.id = n.server_id）。
// 订阅线路、套餐页每个池的可下发数、后台节点列表的「是否下发」都用它，不另写变体。
const DeliverableNodeCond = `n.deleted_at IS NULL AND n.serving_status = 'active' AND n.traffic_exceeded_at IS NULL
 AND s.deleted_at IS NULL AND s.status = 'ready' AND s.last_heartbeat_at IS NOT NULL`

// ServedOnServerCond 是「一台服务器上该跑哪些节点」的唯一口径：节点未删除且在役、本周期流量没超限（超限的节点端停掉入站），服务器未删除、
// 处于 ready / draining / maintenance（排空与维护中照常服务已有用户，只是不再发给新拉取的订阅）。
// 节点通道下发的名单（Rosters）与配置（ServerConfigs）按同一个节点集合给；不在集合里的节点由节点端停掉。
//
// 别名约定同 DeliverableNodeCond（n、s）。
const ServedOnServerCond = `n.deleted_at IS NULL AND n.serving_status = 'active' AND n.traffic_exceeded_at IS NULL
 AND s.deleted_at IS NULL AND s.status IN ('ready', 'draining', 'maintenance')`
