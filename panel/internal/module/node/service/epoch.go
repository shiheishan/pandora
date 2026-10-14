package service

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// 下发纪元（docs/architecture.md 第 13 节）。会影响节点名单或节点配置的写，在事务里登记，
// 提交后 INCR 对应纪元并 PUBLISH 一条通知；会话管理器收到通知就重算，另外每 5 秒比对一次纪元值兜底，
// 所以丢一条 pub/sub 最多晚 5 秒。回滚不发。
const (
	// UsersEpochKey 是名单纪元：订阅、配额、流量包、用户状态与用户组、节点池与池的用户组、节点在役状态变了就推进。
	UsersEpochKey = "node:users_epoch"
	// ConfigEpochKey 是配置纪元：节点协议、端口、地址、所在服务器、路由变了就推进。
	ConfigEpochKey = "node:config_epoch"
	// ChangedChannel 是通知频道，载荷是 "users" 或 "config"。
	ChangedChannel = "node:changed"
)

// MarkUsersChanged 登记：本事务提交后推进名单纪元并通知（同一事务里多次调用只发一次）。
func (s *Service) MarkUsersChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "node:users_changed", func(ctx context.Context) { s.bump(ctx, UsersEpochKey, "users") })
}

// MarkConfigChanged 登记：本事务提交后推进配置纪元并通知（同一事务里多次调用只发一次）。
func (s *Service) MarkConfigChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "node:config_changed", func(ctx context.Context) { s.bump(ctx, ConfigEpochKey, "config") })
}

// MarkLinesChanged 登记：本事务提交后清掉本进程的订阅线路缓存（delivery_cache.go）。只影响订阅线路、不影响节点端配置与名单的写用它
// （中转、标签、倍率）；别的进程的缓存最多 deliveryCacheTTL 后自然过期。
func (s *Service) MarkLinesChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "node:lines_changed", func(context.Context) { s.lineCache.purge() })
}

// SubscriptionsChanged 实现 subscription 的 RosterNotifier：订阅里影响名单的事实变了（已在提交后调用）。
func (s *Service) SubscriptionsChanged(ctx context.Context, _ []uuid.UUID) {
	s.bump(ctx, UsersEpochKey, "users")
}

// bump 推进纪元并发通知。Redis 出错只记日志：会话管理器的纪元比对与 15 秒全量比对兜底。
func (s *Service) bump(ctx context.Context, key, kind string) {
	if err := s.bumpErr(ctx, key, kind); err != nil {
		plog.From(ctx).Warn("推进节点下发纪元失败", zap.String("kind", kind), zap.Error(err))
	}
}

// bumpErr 推进纪元并发通知，把 Redis 错误交给调用方（要据此决定是否重试的周期任务用）。
func (s *Service) bumpErr(ctx context.Context, key, kind string) error {
	// 本进程缓存的订阅线路可能已经过时（delivery_cache.go），先清掉
	s.lineCache.purge()
	if s.rdb == nil {
		return nil
	}
	pipe := s.rdb.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Publish(ctx, ChangedChannel, kind)
	_, err := pipe.Exec(ctx)
	return err
}
