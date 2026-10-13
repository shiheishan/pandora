package service

// 设备数限制（p5-devices）：判定策略读写、在线设备总览、strict 名单过滤与周期重判、订阅的在线设备数。
//
// 两种模式：loose 下设备数随名单下发，每个节点各自限；strict 另把跨节点去重后在线设备超过「上限+宽容」的订阅
// 从所有池的名单里整条摘掉（poolRosters 调 strictExcluded）。超限只经 deviceExceeded 判，上限只经
// effectiveDeviceLimit（与名单下发的设备数同一口径）取。名单是推送的，strict 下超限集合随在线数变化，
// 由周期任务 devices:strict 调 RecheckStrictDevices 比对摘要、变了推进名单纪元。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// DeviceSettings 是设备限制策略的读写面，由 settings service 实现。
type DeviceSettings interface {
	DevicePolicy(ctx context.Context) (settingssvc.DevicePolicy, error)
	SetDevicePolicy(ctx context.Context, u settingssvc.DevicePolicyUpdate) error
}

// DeviceSubscriptions 是按 id 批量读订阅，由 subscription service 实现。
type DeviceSubscriptions interface {
	SubscriptionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]subsvc.Subscription, error)
}

// DeviceUsers 是批量取邮箱，由 identity service 实现。
type DeviceUsers interface {
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
}

// DevicePlans 是批量取套餐（展示名），由 catalog service 实现。
type DevicePlans interface {
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
}

var (
	_ DeviceSettings             = (*settingssvc.Service)(nil)
	_ DeviceSubscriptions        = (*subsvc.Service)(nil)
	_ DeviceUsers                = (*identitysvc.Service)(nil)
	_ DevicePlans                = (*catalogsvc.Service)(nil)
	_ subsvc.OnlineDeviceCounter = (*Service)(nil)
)

// UseDevices 只在启动装配时调用（不并发安全）。套餐版本的设备上限沿用 UseDelivery 装配的 PlanVersions。
func (s *Service) UseDevices(st DeviceSettings, subs DeviceSubscriptions, users DeviceUsers, plans DevicePlans) {
	s.devSettings, s.devSubs, s.devUsers, s.devPlans = st, subs, users, plans
}

// deviceOverviewLimit 是在线设备总览最多列出的订阅数（与旧接口相同）。
const deviceOverviewLimit = 200

// StrictDigestKey 是 strict 周期任务记下的上一轮超限订阅集合摘要（Redis，node 模块键前缀）。
const StrictDigestKey = "node:devices_strict_digest"

// strictDigestTTL 是摘要的存活期：任务每分钟刷新一次，停跑一小时后自然过期。
const strictDigestTTL = time.Hour

// OnlineDevice 是在线设备总览的一行（一份订阅）。
type OnlineDevice struct {
	SubscriptionID uuid.UUID
	UserID         uuid.UUID
	Email          string
	// Plan 是套餐名
	Plan string
	// Limit 是生效的设备上限：订阅覆盖 → 套餐版本 max_devices → 0；0 = 不限
	Limit int
	// Online 是窗口内跨节点去重的设备数；Nodes 是有在线记录的节点数
	Online int
	Nodes  int
	// Overridden 是订阅上有管理员覆盖
	Overridden bool
	// Exceeded = Limit > 0 && Online > Limit + 宽容（两种模式都这么算，只是展示）
	Exceeded   bool
	LastSeenAt time.Time
}

// DeviceOverview 是在线设备总览与当前生效的判定策略。
type DeviceOverview struct {
	Devices       []OnlineDevice
	Mode          string
	Grace         int
	WindowMinutes int
}

// DevicePolicyInput 是后台改判定策略的输入：Mode 必填；Grace、WindowMinutes 为 nil 表示不改。
type DevicePolicyInput struct {
	Mode          string
	Grace         *int
	WindowMinutes *int
}

// deviceExceeded 是超限的唯一判定：上限为 0（不限）永不超限；在线设备数超过「上限+宽容」才算。
// 宽容吸收 IP 抖动：手机切网络会让同一台设备短暂占两个地址。
func deviceExceeded(limit, online, grace int) bool {
	return limit > 0 && online > limit+grace
}

// effectiveDeviceLimit 是一份订阅生效的设备上限（与名单下发的设备数同一口径，见 rosterLimits）；0 = 不限。
func effectiveDeviceLimit(subLimit *int, v catalogsvc.Version) int {
	_, d := rosterLimits(subLimit, v)
	return int(d)
}

// devicePolicy 读判定策略；没装配或读失败按缺省（loose、宽容 1、窗口 5 分钟）并记 warn：
// 配置读不出来不该把所有人当成超限踢下线（与旧实现一致）。
func (s *Service) devicePolicy(ctx context.Context) settingssvc.DevicePolicy {
	if s.devSettings == nil {
		return settingssvc.DefaultDevicePolicy()
	}
	p, err := s.devSettings.DevicePolicy(ctx)
	if err != nil {
		plog.From(ctx).Warn("读取设备限制策略失败，按缺省（loose）", zap.Error(err))
		return settingssvc.DefaultDevicePolicy()
	}
	return p
}

// onlineWindow 是「在线」的时间窗：设备识别窗口（设置 devices.window_minutes）。
func (s *Service) onlineWindow(ctx context.Context) time.Duration {
	return s.devicePolicy(ctx).Window()
}

// strictDigest 是一组订阅 id 的摘要：按字节序排序后每个 id 一行，SHA-256 小写 hex；与输入顺序无关。
func strictDigest(ids []uuid.UUID) string {
	sorted := slices.Clone(ids)
	slices.SortFunc(sorted, compareUUID)
	h := sha256.New()
	for _, id := range sorted {
		h.Write([]byte(id.String()))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// OnlineDevices 是后台在线设备总览：窗口内有在线设备的订阅，按跨节点去重设备数降序，最多 200 条，附当前策略。
// 读数一次、订阅一次、邮箱一次、套餐一次、版本一次，与行数无关。订阅行已不存在的跳过。
func (s *Service) OnlineDevices(ctx context.Context) (DeviceOverview, error) {
	if s.devSettings == nil || s.devSubs == nil || s.devUsers == nil || s.devPlans == nil || s.versions == nil {
		return DeviceOverview{}, errDeliveryPortMissing
	}
	p, err := s.devSettings.DevicePolicy(ctx)
	if err != nil {
		return DeviceOverview{}, err
	}
	out := DeviceOverview{Devices: []OnlineDevice{}, Mode: p.Mode, Grace: p.Grace, WindowMinutes: p.WindowMinutes}
	rows, err := s.repo.OnlineDeviceOverview(ctx, s.now().Add(-p.Window()), deviceOverviewLimit)
	if err != nil {
		return DeviceOverview{}, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	subIDs := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		subIDs[i] = r.SubscriptionID
	}
	subs, err := s.devSubs.SubscriptionsByIDs(ctx, subIDs)
	if err != nil {
		return DeviceOverview{}, err
	}
	var userIDs, planIDs, versionIDs []uuid.UUID
	for _, sub := range subs {
		userIDs = append(userIDs, sub.UserID)
		planIDs = append(planIDs, sub.PlanID)
		versionIDs = append(versionIDs, sub.PlanVersionID)
	}
	userIDs, planIDs, versionIDs = uniqueUUIDs(userIDs), uniqueUUIDs(planIDs), uniqueUUIDs(versionIDs)

	emails := map[uuid.UUID]string{}
	plans := map[uuid.UUID]catalogsvc.Plan{}
	versions := map[uuid.UUID]catalogsvc.Version{}
	if len(userIDs) > 0 {
		if emails, err = s.devUsers.Emails(ctx, userIDs); err != nil {
			return DeviceOverview{}, err
		}
	}
	if len(planIDs) > 0 {
		if plans, err = s.devPlans.PlansByIDs(ctx, planIDs); err != nil {
			return DeviceOverview{}, err
		}
	}
	if len(versionIDs) > 0 {
		if versions, err = s.versions.VersionsByIDs(ctx, versionIDs); err != nil {
			return DeviceOverview{}, err
		}
	}

	for _, r := range rows {
		sub, ok := subs[r.SubscriptionID]
		if !ok {
			continue
		}
		limit := effectiveDeviceLimit(sub.DeviceLimit, versions[sub.PlanVersionID])
		out.Devices = append(out.Devices, OnlineDevice{
			SubscriptionID: sub.ID,
			UserID:         sub.UserID,
			Email:          emails[sub.UserID],
			Plan:           plans[sub.PlanID].Name,
			Limit:          limit,
			Online:         r.Online,
			Nodes:          r.Nodes,
			Overridden:     sub.DeviceLimit != nil,
			Exceeded:       deviceExceeded(limit, r.Online, p.Grace),
			LastSeenAt:     r.LastSeenAt.UTC(),
		})
	}
	return out, nil
}

// SetDevicePolicy 是后台改判定策略：校验在端口判空之前；设置与名单纪元在同一事务里登记，
// 模式、宽容、窗口都影响 strict 名单，提交后推进名单纪元。
func (s *Service) SetDevicePolicy(ctx context.Context, in DevicePolicyInput) error {
	u := settingssvc.DevicePolicyUpdate{Mode: in.Mode, Grace: in.Grace, WindowMinutes: in.WindowMinutes}
	if err := settingssvc.ValidateDevicePolicyUpdate(u); err != nil {
		return err
	}
	if s.devSettings == nil {
		return errDeliveryPortMissing
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.devSettings.SetDevicePolicy(ctx, u); err != nil {
			return err
		}
		s.MarkUsersChanged(ctx)
		return nil
	})
}

// OnlineDeviceCounts 实现 subscription 的 OnlineDeviceCounter：窗口内跨节点去重的设备数，没有在线记录的不在结果里。
func (s *Service) OnlineDeviceCounts(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	if len(subIDs) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	return s.repo.DeviceCounts(ctx, subIDs, s.now().Add(-s.onlineWindow(ctx)))
}

// strictExcluded 是 strict 模式下要从名单里摘掉的订阅（subs、versions 是 poolRosters 已读出的，只多一次在线计数）；
// loose 返回 nil。上限至少 1、超限要 > 上限+宽容，所以只取设备数 > 宽容+1 的一小撮。
func (s *Service) strictExcluded(ctx context.Context, subs []subsvc.ServingSubscription, versions map[uuid.UUID]catalogsvc.Version) (map[uuid.UUID]bool, error) {
	p := s.devicePolicy(ctx)
	if !p.Strict() {
		return nil, nil
	}
	counts, err := s.repo.DeviceCountsAbove(ctx, s.now().Add(-p.Window()), p.Grace+1)
	if err != nil {
		return nil, err
	}
	if len(counts) == 0 {
		return nil, nil
	}
	out := map[uuid.UUID]bool{}
	for _, sub := range subs {
		c, ok := counts[sub.ID]
		if !ok {
			continue
		}
		if deviceExceeded(effectiveDeviceLimit(sub.DeviceLimit, versions[sub.PlanVersionID]), c, p.Grace) {
			out[sub.ID] = true
		}
	}
	return out, nil
}

// RecheckStrictDevices 由周期任务 devices:strict（每分钟）调：名单是推送的，strict 下超限集合随在线数变化而变，
// 这里比对超限集合的摘要，变了就推进名单纪元让会话层重算；loose 时删掉摘要键、什么都不做。
// 任务里没有事务，直接 bump（与 UsersChanged 同一个非事务入口）。
func (s *Service) RecheckStrictDevices(ctx context.Context) error {
	if s.rdb == nil {
		return nil
	}
	if s.devSettings == nil || s.devSubs == nil || s.versions == nil {
		return errDeliveryPortMissing
	}
	p, err := s.devSettings.DevicePolicy(ctx)
	if err != nil {
		return err
	}
	if !p.Strict() {
		return s.rdb.Del(ctx, StrictDigestKey).Err()
	}
	ids, err := s.strictExceededIDs(ctx, p)
	if err != nil {
		return err
	}
	digest := strictDigest(ids)
	old, err := s.rdb.Get(ctx, StrictDigestKey).Result()
	switch {
	case errors.Is(err, redis.Nil):
		old = ""
	case err != nil:
		return err
	}
	if err := s.rdb.Set(ctx, StrictDigestKey, digest, strictDigestTTL).Err(); err != nil {
		return err
	}
	if old != digest {
		s.bump(ctx, UsersEpochKey, "users")
		plog.From(ctx).Info("strict 设备超限集合变了，推进名单纪元", zap.Int("exceeded", len(ids)))
	}
	return nil
}

// strictExceededIDs 是当前超限的订阅 id（按字节序）：读数一次、订阅一次、版本一次，只针对设备数 > 宽容+1 的一小撮。
func (s *Service) strictExceededIDs(ctx context.Context, p settingssvc.DevicePolicy) ([]uuid.UUID, error) {
	counts, err := s.repo.DeviceCountsAbove(ctx, s.now().Add(-p.Window()), p.Grace+1)
	if err != nil {
		return nil, err
	}
	if len(counts) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	subs, err := s.devSubs.SubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	versionIDs := make([]uuid.UUID, 0, len(subs))
	for _, sub := range subs {
		versionIDs = append(versionIDs, sub.PlanVersionID)
	}
	versionIDs = uniqueUUIDs(versionIDs)
	versions := map[uuid.UUID]catalogsvc.Version{}
	if len(versionIDs) > 0 {
		if versions, err = s.versions.VersionsByIDs(ctx, versionIDs); err != nil {
			return nil, err
		}
	}
	var out []uuid.UUID
	for _, id := range ids {
		sub, ok := subs[id]
		if !ok {
			continue
		}
		if deviceExceeded(effectiveDeviceLimit(sub.DeviceLimit, versions[sub.PlanVersionID]), counts[id], p.Grace) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, compareUUID)
	return out, nil
}

// uniqueUUIDs 排序去重（原地，返回去重后的切片）。
func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	slices.SortFunc(ids, compareUUID)
	return slices.Compact(ids)
}
