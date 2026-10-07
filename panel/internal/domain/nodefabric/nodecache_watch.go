package nodefabric

import (
	"context"
	"log/slog"
	"time"

	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// nodeUsersInvalidateGap 是表级事件作废用户集的最小间隔（去抖）。
//
// 后台批量改订阅会在一秒内冒出成百上千条 subscriptions 变更；每条都作废，节点
// 每次来拉都要重算，等于没有缓存。第一条立刻作废（付款后的新用户不多等），之后
// 间隔内的只合并成末尾一次。
const nodeUsersInvalidateGap = 2 * time.Second

// usersInvalidatingTables 是 ListNodeUsers 读到、且挂了 zz_notify 触发器的表：
// 它们一变，下发集合就可能变。读到却没挂触发器的（quota_balances、system_settings、
// node_alive_ips、plan_node_pools、node_pool_user_groups、users）要么有显式的
// node.users.changed（R104 三处写路径），要么只能等 TTL。
var usersInvalidatingTables = map[string]bool{
	"subscriptions":       true,
	"plan_versions":       true,
	"traffic_pack_grants": true,
}

// RunNodeCacheInvalidation 订阅租户的变更事件，按事件作废节点链路缓存，阻塞到
// ctx 结束。只有它在跑，该租户的缓存才生效：订阅先建立、再开缓存，退出时先关
// 缓存、再退订——中间任何一刻都不会有「缓存开着却收不到作废」的窗口。
//
// 没挂 realtime 或没调 EnableNodeCaches 时直接返回，缓存保持关闭。
func (s *Service) RunNodeCacheInvalidation(ctx context.Context, tenantID string, log *slog.Logger) {
	c := s.caches
	if c == nil || s.realtime == nil {
		return
	}
	events, unsubscribe := s.realtime.Subscribe([]string{
		realtime.ChannelAdmin(tenantID), realtime.ChannelNodeAll(tenantID),
	})
	defer unsubscribe()
	c.watch(tenantID)
	defer c.unwatch(tenantID)
	if log != nil {
		log.Info("节点链路缓存已开启", "tenant_id", tenantID,
			"用户集TTL", nodeUsersCacheTTL.String(), "认证TTL", nodeAuthCacheTTL.String())
	}

	d := newUsersDebouncer(nodeUsersInvalidateGap, time.Now)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if d.fire() {
				c.invalidateUsers(tenantID)
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			switch applyNodeCacheEvent(c, tenantID, ev) {
			case usersNow:
				d.reset()
				c.invalidateUsers(tenantID)
			case usersDebounced:
				now, wait := d.event()
				if now {
					c.invalidateUsers(tenantID)
				} else if wait > 0 {
					timer.Reset(wait)
				}
			}
		}
	}
}

type usersInvalidation int

const (
	usersUntouched usersInvalidation = iota
	usersNow
	usersDebounced
)

// applyNodeCacheEvent 处理一条事件里与节点认证有关的部分，并告诉调用方用户集要
// 不要作废、怎么作废。拆出来是为了不起 Valkey 也能测。
func applyNodeCacheEvent(c *nodeCaches, tenantID string, ev realtime.Event) usersInvalidation {
	if table, ok := ev.Payload["table"].(string); ok {
		switch {
		case table == "nodes":
			// 节点行任何变化（状态、服务状态、令牌、协议、换池）都作废它的认证缓存。
			// 心跳也会改这一行，代价是每 30 秒多一次未命中，和 TTL 本身同量级。
			if id, _ := ev.Payload["id"].(string); id != "" {
				c.invalidateNode(tenantID, id)
			}
		case usersInvalidatingTables[table]:
			return usersDebounced
		}
		return usersUntouched
	}
	if nodeID, _ := ev.Payload["node_id"].(string); nodeID != "" {
		// 节点配置变了（含换池、改协议、吊销后由后台补发的通知）
		c.invalidateNode(tenantID, nodeID)
		return usersUntouched
	}
	if ev.Topic == realtime.TopicNodeUsersChanged {
		// 显式的「谁能连哪些节点变了」（付款、R104 三处写路径）：不去抖
		return usersNow
	}
	return usersUntouched
}

// usersDebouncer 是「首条立即、间隔内合并成末尾一次」的去抖状态。只在
// RunNodeCacheInvalidation 的单个 goroutine 里用，不加锁。
type usersDebouncer struct {
	gap     time.Duration
	now     func() time.Time
	last    time.Time
	pending bool
}

func newUsersDebouncer(gap time.Duration, now func() time.Time) *usersDebouncer {
	return &usersDebouncer{gap: gap, now: now}
}

// event 记一条事件：返回 now=true 表示立刻作废；否则 wait>0 表示需要在 wait 后
// 补一次（已有待补的就返回 0，不重复定时）。
func (d *usersDebouncer) event() (now bool, wait time.Duration) {
	t := d.now()
	if d.last.IsZero() || t.Sub(d.last) >= d.gap {
		d.last, d.pending = t, false
		return true, 0
	}
	if d.pending {
		return false, 0
	}
	d.pending = true
	return false, d.gap - t.Sub(d.last)
}

// fire 在定时器到点时调用，返回是否要补这一次作废。
func (d *usersDebouncer) fire() bool {
	if !d.pending {
		return false
	}
	d.last, d.pending = d.now(), false
	return true
}

// reset 在一次不去抖的作废之后调用：它已经覆盖了待补的那次。
func (d *usersDebouncer) reset() {
	d.last, d.pending = d.now(), false
}
