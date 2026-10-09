package nodefabric

import "sync"

// 「节点此刻放行的 uid」集合按名单版本只建一次（w10quiet）。
//
// 流量上报只扣名单里的用户（reportTraffic）：原先每份上报都把整份名单装进一张新 map，
// 1 万人、每秒 17 份上报时这是 aegis-node 静默 CPU 的一大块。缓存路径上同池节点拿到的
// 是同一个名单切片（用户集缓存条目共享、只读），按切片身份（首元素地址 + 长度）认出它，
// 集合只建一次；条目引用着那个切片，地址在条目存活期间不会被别的切片复用。直查路径每次
// 都是新切片，照旧现建。判定与每份现建逐个相同。

const servedSetSlots = 8 // 按池计，同时活跃的名单版本不多

type servedSetEntry struct {
	users []ProxyUser
	set   map[int64]struct{}
}

type servedSets struct {
	mu    sync.Mutex
	slots [servedSetSlots]servedSetEntry
	next  int
}

func sameSlice(a, b []ProxyUser) bool {
	return len(a) == len(b) && len(a) > 0 && &a[0] == &b[0]
}

// servedSet 返回 users 的 uid 集合（只读）。
func (s *Service) servedSet(users []ProxyUser) map[int64]struct{} {
	c := s.served
	if c != nil {
		c.mu.Lock()
		for i := range c.slots {
			if sameSlice(c.slots[i].users, users) {
				set := c.slots[i].set
				c.mu.Unlock()
				return set
			}
		}
		c.mu.Unlock()
	}
	set := make(map[int64]struct{}, len(users))
	for _, u := range users {
		set[u.ID] = struct{}{}
	}
	if c != nil && len(users) > 0 {
		c.mu.Lock()
		c.slots[c.next] = servedSetEntry{users: users, set: set}
		c.next = (c.next + 1) % servedSetSlots
		c.mu.Unlock()
	}
	return set
}
