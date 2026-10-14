// Package service 是 node 模块的业务层：服务器与节点的后台编排、节点池、
// 节点通道（接入、会话、名单与配置下发、流量入账），以及给 subscription 的可下发线路。
//
// 状态机写成转换表加 CanTransition（docs/architecture.md 第 5.4 节）；影响节点下发的写，
// 在事务里调 MarkUsersChanged / MarkConfigChanged，提交后才推进纪元（epoch.go）。
package service

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/geoip"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service 是 node 模块对外的唯一入口。别的模块的依赖经 Use* 在启动装配时注入（各主题文件里定义）。
type Service struct {
	repo  *repo.Repo
	tx    txRunner
	audit auditWriter
	rdb   *redis.Client
	now   func() time.Time

	// p5-pools 注入（pools 主题文件里定义接口与 Use* 方法）
	catalog Catalog
	groups  UserGroups
	inUse   VersionsInUse

	// 第二波：会话管理（channel.go；p5-session 的 *Sessions 经 UseSessions 注入）
	sessions SessionControl

	// 第二波 p5-enroll 注入（identity.go 的 UseEnrollment）：节点 CA 与安装命令的对外地址
	enroll enrollState

	// idem 在签安装令牌的事务里以不可重放完成幂等认领（bootstrap.go 的 UseIdempotency）
	idem IdemCompleter

	// 第二波 p5-traffic 注入（traffic_charger.go 的 UseTrafficCharger）：流量扣量
	charger TrafficCharger

	// keys 给在线设备键做 HMAC（online.go 的 UseKeys）
	keys DeviceKeyHasher

	// 第二波 p5-delivery 注入（delivery.go）：名单要的订阅、账号、套餐版本读取面
	subs     ServingSubscriptions
	users    ServingUsers
	versions PlanVersions

	// p5-devices 注入（devices.go 的 UseDevices）：设备限制策略、按 id 读订阅、邮箱、套餐名
	devSettings DeviceSettings
	devSubs     DeviceSubscriptions
	devUsers    DeviceUsers
	devPlans    DevicePlans

	// 节点端一键安装的分发（pdnd_dist.go 的 UsePdndDist）：二进制目录与写进安装脚本的面板地址
	pdnd pdndDist

	geo *geoip.Resolver

	// rt 实时推送（realtime.go 的 UseRealtime）；nil 时不推
	rt *realtime.Publisher

	// lineCache 是订阅线路的微缓存（delivery_cache.go），构造时建好
	lineCache *deliveryCache
}

// New 构造 node service。rdb 用于下发纪元（epoch.go），为 nil 时只跳过通知。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, rdb *redis.Client) *Service {
	return &Service{repo: r, tx: txm, audit: audit, rdb: rdb, now: time.Now,
		lineCache: newDeliveryCache(deliveryCacheSize, deliveryCacheTTL)}
}
