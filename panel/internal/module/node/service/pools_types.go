package service

import (
	"github.com/google/uuid"
)

// 节点池的状态（node_pools.status）。池状态之间可以任意切换：停用的池不能再被绑进套餐版本，
// 已有的绑定不受影响。
const (
	PoolActive   = "active"
	PoolDraining = "draining"
	PoolDisabled = "disabled"
)

// 审计动作名与资源类型（沿用旧代码）。
const (
	ActionPoolCreated           = "node_pool.created"
	ActionPoolUpdated           = "node_pool.updated"
	ActionPoolDeleted           = "node_pool.deleted"
	ActionPoolUserGroupsChanged = "node_pool.user_groups_changed"
	ResourceNodePool            = "node_pool"
)

// MaxPoolUserGroups 是一个池「仅限用户组」名单的上限。
const MaxPoolUserGroups = 100

// MaxPlanPools 是一个套餐版本一次最多绑定的池数。
const MaxPlanPools = 500

// Pool 是后台节点池列表的一行。
type Pool struct {
	ID     uuid.UUID
	Code   string
	Name   string
	Region string
	Status string
	// Nodes 是池里未删除的节点数，ActiveNodes 是其中在役的（serving_status = active，协议必然配好）。
	Nodes       int64
	ActiveNodes int64
	// Plans 是绑定了这个池的套餐版本数；PlanNames 是这些版本的套餐名（去重、升序）。
	Plans     int
	PlanNames []string
	// Members 是池里未删除的节点，按 sort_order、node_no。
	Members []PoolMember
	// AllowedUserGroups 是「仅限用户组」名单（空 = 不限定），按名字、id 升序。
	AllowedUserGroups []PoolUserGroup
}

// PoolMember 是池里的一个节点。
type PoolMember struct {
	ID     uuid.UUID
	Name   string
	NodeNo int64
}

// PoolUserGroup 是池名单里的一个用户组。
type PoolUserGroup struct {
	ID   uuid.UUID
	Name string
}

// PoolCreate 是新建节点池的输入（handler 已去首尾空白）。Code 为空时由名字派生。
type PoolCreate struct {
	Code   string
	Name   string
	Region string
	// UserGroupIDs 为 nil 表示不设名单；非 nil 时已去重排序（空 = 不限定）。
	UserGroupIDs *[]uuid.UUID
}

// PoolUpdate 是修改节点池的输入：空串字段保持原值（名字与地区都不能清空）。
type PoolUpdate struct {
	Name   string
	Region string
	Status string
	// UserGroupIDs 为 nil 表示不改名单；非 nil 时已去重排序（空 = 取消限定）。
	UserGroupIDs *[]uuid.UUID
}

// PlanPoolOption 是套餐页绑池选择器里的一个池。
type PlanPoolOption struct {
	ID   uuid.UUID
	Name string
	// ActiveNodes 与节点池列表同口径；DeliverableNodes 是其中真能写进订阅的（repo.DeliverableNodeCond）。
	// 池限定用户组时组外用户拿到的更少，这一半与用户有关，不在这里算。
	ActiveNodes      int64
	DeliverableNodes int64
	Bound            bool
}

// PlanPools 是套餐页绑池选择器：有草稿版本给草稿（可编辑），否则给当前已发布版本（只读）。
// 没有任何版本时 VersionID 为 nil，Pools 仍列出可选的池且都未绑定。
type PlanPools struct {
	VersionID     *uuid.UUID
	VersionStatus string
	RowVersion    int64
	Editable      bool
	Pools         []PlanPoolOption
}
