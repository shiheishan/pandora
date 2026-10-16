package service

// 下发资格：名单（roster.go）与订阅线路（node_source.go）共用的依赖与规则。
//
// 资格由几块拼成，每块只有一个口径：
//   - 订阅侧可服务（生效中、流量没耗尽）：subscription 的 ServingSubscriptions；线路由拉取路径先判过；
//   - 账号 active：identity 的 ActiveUserGroups（封禁即断，解封恢复）；线路由拉取路径先判过；
//   - 套餐绑定了节点所在的池：catalog 的 PlanPools（订阅按版本找到套餐）；没划池的节点不服务任何人（fail closed）；
//   - 池的用户组限定：poolAdmits（本文件，名单与线路都只经它判）；
//   - 节点自身：名单按 repo.ServedOnServerCond，线路按 repo.DeliverableNodeCond，再按 preferFresh 分层。
//
// 节点池的状态（active / draining / disabled）不影响名单与线路，池状态只管「能不能再被绑进套餐」，
// 已有绑定照常服务。要停一个池的服务，停用池里的节点或从套餐解绑。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ServingSubscriptions 是名单要的订阅侧读取面，由 subscription service 实现。
type ServingSubscriptions interface {
	ServingSubscriptions(ctx context.Context) ([]subsvc.ServingSubscription, error)
}

// ServingUsers 是名单要的账号读取面，由 identity service 实现。
type ServingUsers interface {
	ActiveUserGroups(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*uuid.UUID, error)
}

// PlanVersions 是名单要的套餐版本读取面（限速、设备数），由 catalog service 实现。
type PlanVersions interface {
	VersionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Version, error)
}

var (
	_ ServingSubscriptions       = (*subsvc.Service)(nil)
	_ ServingUsers               = (*identitysvc.Service)(nil)
	_ PlanVersions               = (*catalogsvc.Service)(nil)
	_ subsvc.NodeSource          = (*Service)(nil)
	_ identitysvc.RosterNotifier = (*Service)(nil)
)

// UseDelivery 只在启动装配时调用（不并发安全）。没装配时 Rosters 回 Unavailable。
func (s *Service) UseDelivery(subs ServingSubscriptions, users ServingUsers, versions PlanVersions) {
	s.subs, s.users, s.versions = subs, users, versions
}

// errDeliveryPortMissing 是启动时没装配名单或线路要的模块端口。
var errDeliveryPortMissing = errs.Newf(errs.Unavailable, "节点下发依赖的模块未装配")

// HeartbeatFreshWindow 是线路分层里「心跳新鲜」的窗口：服务器心跳在这之内算新鲜。
// 比后台列表的在线判定宽得多：它决定用户能不能拿到线路，误杀的代价是用户没得用，所以要保守。
const HeartbeatFreshWindow = 10 * time.Minute

// poolAdmits 是节点池「仅限用户组」的唯一判定：名单空 = 不限定；否则用户所在的组要在名单里。
// 默认组（userGroup 为 nil）的用户进不了任何限定了组的池。名单与线路都只经它判，不另写变体（SQL 里也不写）。
func poolAdmits(poolGroups []uuid.UUID, userGroup *uuid.UUID) bool {
	if len(poolGroups) == 0 {
		return true
	}
	if userGroup == nil {
		return false
	}
	return slices.Contains(poolGroups, *userGroup)
}

// rosterLimits 算一份订阅在节点上的限速与设备数：限速取套餐版本的 throttle_kbps（没有 = 0 不限）；
// 设备数 = coalesce(订阅的覆盖, 版本的 max_devices, 0)，0 = 不限。设备数两种模式都下发（节点端各自限）；
// strict 另在 poolRosters 里把跨节点超限的订阅整条摘掉（devices.go）。
func rosterLimits(subDeviceLimit *int, v catalogsvc.Version) (speedKbps, devices uint32) {
	if v.ThrottleKbps != nil {
		speedKbps = clampUint32(*v.ThrottleKbps)
	}
	switch {
	case subDeviceLimit != nil:
		devices = clampUint32(*subDeviceLimit)
	case v.MaxDevices != nil:
		devices = clampUint32(*v.MaxDevices)
	}
	return speedKbps, devices
}

// clampUint32 把非正数当 0，超过 uint32 上限的截到上限（库里的约束远小于它，只防御）。
func clampUint32(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if int64(n) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n) //nolint:gosec // 上面已限制在 [1, MaxUint32]
}

// rosterVersionPrefix 是名单版本的格式前缀；规范编码改了就换前缀。
const rosterVersionPrefix = "u1-"

// rosterVersion 是名单的内容版本："u1-" + 小写 hex(SHA-256(规范编码))。同样的用户集合（含限速、设备数）
// 得到同样的版本，与节点、服务器、输入顺序无关。
//
// 规范编码：按 UID 升序，每个用户一行 "<uid>,<uuid>,<限速 kbps>,<设备数>\n"，
// uid、限速、设备数是十进制整数，uuid 是 36 字符的小写带连字符写法；空名单是空串。
// 各字段里不会出现逗号与换行，所以编码无歧义。
func rosterVersion(users []RosterUser) string {
	if !slices.IsSortedFunc(users, compareRosterUser) {
		users = slices.Clone(users)
		slices.SortFunc(users, compareRosterUser)
	}
	h := sha256.New()
	buf := make([]byte, 0, 80)
	for _, u := range users {
		buf = buf[:0]
		buf = strconv.AppendInt(buf, u.UID, 10)
		buf = append(buf, ',')
		buf = append(buf, u.UUID.String()...)
		buf = append(buf, ',')
		buf = strconv.AppendUint(buf, uint64(u.SpeedLimitKbps), 10)
		buf = append(buf, ',')
		buf = strconv.AppendUint(buf, uint64(u.DeviceLimit), 10)
		buf = append(buf, '\n')
		h.Write(buf)
	}
	return rosterVersionPrefix + hex.EncodeToString(h.Sum(nil))
}

func compareRosterUser(a, b RosterUser) int {
	switch {
	case a.UID < b.UID:
		return -1
	case a.UID > b.UID:
		return 1
	}
	return 0
}

// compareUUID 按字节序比较（与 PostgreSQL uuid 的排序一致）。
func compareUUID(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }

// deliveryCandidate 是一条候选线路与它的分层事实。
type deliveryCandidate struct {
	node render.Node
	// fresh 是服务器心跳在 HeartbeatFreshWindow 之内
	fresh bool
	// degraded 是节点报告入站没按期望在服务（repo.NodeDegradedCond）
	degraded bool
}

// preferFresh 按新鲜度分层挑线路，保持输入顺序，永不为 nil：
//  1. 先摘降级节点（节点自己说这条线路不通），有不降级的就只看它们，全是降级的照发；
//  2. 再分新鲜：有心跳新鲜的就只给新鲜的，一个都没有就给上一步剩下的全部。
//
// 不直接把超时的过滤掉：空订阅会让客户端清空服务器列表，比「可能连不上」更糟；
// 心跳失联常是面板侧的观测问题（agent 崩了、上报链路被挡），节点本身还在转发。
func preferFresh(cands []deliveryCandidate) []render.Node {
	pool := cands
	if healthy := filterCandidates(pool, func(c deliveryCandidate) bool { return !c.degraded }); len(healthy) > 0 {
		pool = healthy
	}
	if fresh := filterCandidates(pool, func(c deliveryCandidate) bool { return c.fresh }); len(fresh) > 0 {
		pool = fresh
	}
	out := make([]render.Node, len(pool))
	for i, c := range pool {
		out[i] = c.node
	}
	return out
}

func filterCandidates(cands []deliveryCandidate, keep func(deliveryCandidate) bool) []deliveryCandidate {
	out := make([]deliveryCandidate, 0, len(cands))
	for _, c := range cands {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}
