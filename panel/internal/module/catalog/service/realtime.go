package service

// 目录的实时推送：plans、plan_versions、prices 的增删改（含发布、改价、改线路、归档）
// 推 plans.changed 给门户公共频道与管理端，门户套餐页与后台套餐页据此重拉。
// 在写成功的事务里调，提交后才真正发出（realtime.Publisher 内部用 tx.AfterCommit），回滚不发；
// 同一事务里同一行只发一次。

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 推送载荷里的表名。
const (
	rtTablePlans    = "plans"
	rtTableVersions = "plan_versions"
	rtTablePrices   = "prices"
)

// UseRealtime 接上实时推送（启动装配时调用，不并发安全）。没接上时不推。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// catalogChanged 在当前事务提交后推一条目录行变更。table 取上面的 rtTable* 之一，op 取 realtime.Op*。
func (s *Service) catalogChanged(ctx context.Context, table, op string, id uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicPlans, table, op, id, nil)
}
