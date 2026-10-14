package service

// node 要的目录（catalog）与用户组（identity）读写面，以及注入方法；
// 另有 node 给 catalog 实现的端口的编译期检查。

import (
	"context"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// Catalog 是 node 要的目录读写面（套餐版本绑池，表归 catalog），由 catalog service 实现。
type Catalog interface {
	// VersionPools 批量取版本绑定的池（一条 SQL）。
	VersionPools(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error)
	// PoolUsage 批量取池被套餐版本引用的情况（一条 SQL）。
	PoolUsage(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID]catalogsvc.PoolUsage, error)
	// PoolBindingVersion 取套餐页绑池选择器对应的版本（草稿优先，否则当前发布版本）。
	PoolBindingVersion(ctx context.Context, planID uuid.UUID) (catalogsvc.PoolBindingVersion, error)
	// SetVersionPools 整体替换草稿版本的绑池（调用方已锁好池），返回版本新的 row_version。
	SetVersionPools(ctx context.Context, planID, versionID uuid.UUID, expectedRowVersion int64, poolIDs []uuid.UUID) (int64, error)
}

// UserGroups 是 node 要的用户组读取面，由 identity service 实现（组很少，取全量）。
type UserGroups interface {
	ListGroups(ctx context.Context) ([]identitysvc.Group, error)
}

// VersionsInUse 是 node 要的订阅侧读取面：哪些套餐版本有生效订阅，由 subscription service 实现。
type VersionsInUse interface {
	VersionsInUse(ctx context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

var (
	_ Catalog                = (*catalogsvc.Service)(nil)
	_ UserGroups             = (*identitysvc.Service)(nil)
	_ VersionsInUse          = (*subsvc.Service)(nil)
	_ catalogsvc.PublishGate = (*Service)(nil)
	_ catalogsvc.PoolGuard   = (*Service)(nil)
)

// UseCatalog 只在启动装配时调用（不并发安全）。
func (s *Service) UseCatalog(c Catalog) { s.catalog = c }

// UseUserGroups 只在启动装配时调用（不并发安全）。
func (s *Service) UseUserGroups(g UserGroups) { s.groups = g }

// UseVersionsInUse 只在启动装配时调用（不并发安全）。
func (s *Service) UseVersionsInUse(v VersionsInUse) { s.inUse = v }
