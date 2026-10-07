package nodefabric

import (
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// servingNodeWithUsers 给测试里的节点挂一份放行名单：ReportTraffic 只给节点当前名单里的
// uid 记账（ListNodeUsers）。这里开缓存，把名单直接放进按池的用户集缓存（纪元取最大、
// 寿命一小时），记账测试不必为了名单去搭套餐、池与服务器的整套夹具。
func servingNodeWithUsers(svc *Service, tenantID, nodeID string, rate float64, uids ...int64) *ServingNode {
	svc.EnableNodeCaches()
	svc.caches.users.ttl = time.Hour
	pool := uuid.NewSHA1(uuid.NameSpaceOID, []byte(tenantID+"/"+nodeID)).String()
	users := make([]ProxyUser, len(uids))
	for i, uid := range uids {
		users[i] = ProxyUser{ID: uid, UUID: fmt.Sprintf("00000000-0000-4000-8000-%012d", uid)}
	}
	svc.caches.users.mu.Lock()
	svc.caches.users.storeLocked(usersCacheKey(tenantID, pool),
		nodeUserSet{users: users, version: UserSetVersion(users), epoch: math.MaxInt64})
	svc.caches.users.mu.Unlock()
	return &ServingNode{ID: nodeID, TrafficRate: rate, PoolID: &pool, epochKnown: true}
}
