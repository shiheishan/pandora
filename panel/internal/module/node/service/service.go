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

	// 目录与用户组的读写面（pools_ports.go 的 UseCatalog、UseUserGroups、UseVersionsInUse）
	catalog Catalog
	groups  UserGroups
	inUse   VersionsInUse

	// 会话管理（*Sessions 经 channel.go 的 UseSessions 注入）
	sessions SessionControl

	// 节点 CA 与安装命令的对外地址（identity.go 的 UseEnrollment）
	enroll enrollState

	// idem 在签安装令牌的事务里以不可重放完成幂等认领（bootstrap.go 的 UseIdempotency）
	idem IdemCompleter

	// 流量扣量（traffic_charger.go 的 UseTrafficCharger）
	charger TrafficCharger

	// keys 给在线设备键做 HMAC（online.go 的 UseKeys）
	keys DeviceKeyHasher

	// 名单要的订阅、账号、套餐版本读取面（delivery.go 的 UseDelivery）
	subs     ServingSubscriptions
	users    ServingUsers
	versions PlanVersions

	// 设备限制策略、按 id 读订阅、邮箱、套餐名（devices.go 的 UseDevices）
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

	// site 读站点时区（xb_ports.go 的 UseSiteSettings）：节点流量周期与时段倍率用
	site SiteSettings
	// echSealer 封存 ECH 私钥（xb_ports.go 的 UseECHSealer）
	echSealer ECHSealer
}

// New 构造 node service。rdb 用于下发纪元（epoch.go），为 nil 时只跳过通知。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, rdb *redis.Client) *Service {
	return &Service{repo: r, tx: txm, audit: audit, rdb: rdb, now: time.Now,
		lineCache: newDeliveryCache(deliveryCacheSize, deliveryCacheTTL)}
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
// 不算必需：sessions 与 enroll（节点通道只在 serve 里装配，见 app/node_channel.go，worker 与 admin 子命令没有）；
// geo（没放 IP 归属地数据文件时就是 nil，归属地留空）；pdnd（UsePdndDist 是配置值，不是端口）。
func (s *Service) MissingPorts() []string {
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"Catalog", s.catalog != nil},
		{"UserGroups", s.groups != nil},
		{"VersionsInUse", s.inUse != nil},
		{"IdemCompleter", s.idem != nil},
		{"TrafficCharger", s.charger != nil},
		{"DeviceKeyHasher", s.keys != nil},
		{"ServingSubscriptions", s.subs != nil},
		{"ServingUsers", s.users != nil},
		{"PlanVersions", s.versions != nil},
		{"DeviceSettings", s.devSettings != nil},
		{"DeviceSubscriptions", s.devSubs != nil},
		{"DeviceUsers", s.devUsers != nil},
		{"DevicePlans", s.devPlans != nil},
		{"Realtime", s.rt != nil},
		{"SiteSettings", s.site != nil},
		{"ECHSealer", s.echSealer != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}
