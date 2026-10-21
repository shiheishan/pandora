package service

// 池的在用套餐：后台节点列表的 granted_plans / 下发说明与上线提示共用一个口径。

import (
	"context"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
)

// poolLivePlans 返回每个池的在用套餐名（去重升序）：池被在售版本绑定，或被有人在用的停售版本绑定。
// 固定两次批量读（catalog PoolUsage、停售版本是否在用），与池个数无关；poolIDs 为空时不读库。
func (s *Service) poolLivePlans(ctx context.Context, poolIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	if len(poolIDs) == 0 {
		return map[uuid.UUID][]string{}, nil
	}
	usage, err := s.catalog.PoolUsage(ctx, poolIDs)
	if err != nil {
		return nil, err
	}
	return s.livePlansFromUsage(ctx, usage)
}

// livePlansFromUsage 在已读到的池绑定上补一次「停售版本是否有人在用」的批量读，再算在用套餐名；
// 节点列表自己也要 usage，所以拆出来避免重复读 PoolUsage。
func (s *Service) livePlansFromUsage(ctx context.Context, usage map[uuid.UUID]catalogsvc.PoolUsage) (map[uuid.UUID][]string, error) {
	var notOnSale []uuid.UUID
	for _, u := range usage {
		for _, b := range u.Bindings {
			if !b.OnSale {
				notOnSale = append(notOnSale, b.VersionIDs...)
			}
		}
	}
	notOnSale = dedupeUUIDs(notOnSale)
	var inUse map[uuid.UUID]bool
	if len(notOnSale) > 0 {
		var err error
		inUse, err = s.inUse.VersionsInUse(ctx, notOnSale)
		if err != nil {
			return nil, err
		}
	}
	return livePlanNames(usage, inUse), nil
}
