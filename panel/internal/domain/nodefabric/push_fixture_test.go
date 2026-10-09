package nodefabric

import (
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/platform/cache"
)

// servingNodeWithUsers 给测试里的节点挂一份放行名单：ReportTraffic 只给节点当前名单里的
// uid 记账（ListNodeUsers）。这里开缓存，把名单直接放进按池的用户集缓存（纪元取最大、
// 寿命一小时），记账测试不必为了名单去搭套餐、池与服务器的整套夹具。
func servingNodeWithUsers(svc *Service, tenantID, nodeID string, rate float64, uids ...int64) *ServingNode {
	svc.EnableNodeCaches()
	if svc.caches.users.Len() == 0 {
		// 名单寿命拉到一小时，其余参数与 newNodeCaches 相同（同一个 svc 上挂多个节点时只换一次）
		svc.caches.users = cache.New[string](cache.Options[nodeUserSet]{TTL: time.Hour, Max: nodeUsersCacheMax,
			StaleGrace: nodeUsersStaleGrace,
			Rank:       func(set nodeUserSet) int64 { return set.epoch },
			Expiry:     func(set nodeUserSet) time.Time { return set.nextExpiry }})
	}
	pool := uuid.NewSHA1(uuid.NameSpaceOID, []byte(tenantID+"/"+nodeID)).String()
	users := make([]ProxyUser, len(uids))
	for i, uid := range uids {
		users[i] = ProxyUser{ID: uid, UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", uid)}
	}
	svc.caches.users.Put(usersCacheKey(tenantID, pool),
		nodeUserSet{users: users, version: UserSetVersion(users), epoch: math.MaxInt64})
	return &ServingNode{ID: nodeID, TrafficRate: rate, PoolID: &pool, epochKnown: true}
}
