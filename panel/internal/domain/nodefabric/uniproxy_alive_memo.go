package nodefabric

import (
	"sync"
	"time"
)

// 在线上报的刷新备忘（w10quiet）。
//
// 节点每分钟报一次在线 IP，而库里已有的行只在 last_seen_at 落后超过 aliveRefresh（2 分钟）
// 才改写（writeAliveRows 的 WHERE）。所以同一批设备一分钟后再报一次时，那条语句什么都
// 不改，却照样占一个事务：1000 个节点、30% 在线时每秒约 17 个。
//
// aegis-node 记下每一行最近一次「真被插入或刷新」的时刻（语句的 RETURNING 带回来，不是
// 猜的）。一份上报的每一行都在 aliveRefreshEvery 之内刷新过，库里那条语句注定什么都不改，
// 就不发；有任何一行是新的或已经到了刷新点，照旧整份写。库里的结果与每份都写逐行相同，
// 新设备照旧当场落库；回执的 ips 是整份的条数（被跳过的行都是此前认下过的）。
//
// 只记真被刷新过的行：认下了但没改写的行，库里的 last_seen_at 介于两分钟前与现在之间、
// 具体不知道，不记，下一份上报照旧写（不改写或刚好刷新），之后就对齐了。备忘按时刻自然
// 失效，不需要通知；进程重启后第一份上报照旧写。

// aliveRefreshEvery 与 aliveRefresh（SQL 里的 interval）是同一个粒度，测试钉着两者相等。
const aliveRefreshEvery = 2 * time.Minute

// aliveMemoMax 是备忘的总行数上限；满了就不再记（退回每份都写），不让内存跟着在线数无限涨。
const aliveMemoMax = 1_000_000

type aliveMemoKey struct {
	uid  int64
	hash [32]byte
}

type aliveMemo struct {
	mu    sync.Mutex
	nodes map[string]map[aliveMemoKey]time.Time // 键：租户 \x00 节点
	size  int
}

func newAliveMemo() *aliveMemo { return &aliveMemo{nodes: make(map[string]map[aliveMemoKey]time.Time)} }

func aliveKeyOf(uid int64, hash []byte) aliveMemoKey {
	k := aliveMemoKey{uid: uid}
	copy(k.hash[:], hash)
	return k
}

// covers 报告这份上报的每一行是否都在 aliveRefreshEvery 之内被刷新过。
func (m *aliveMemo) covers(tenantID, nodeID string, uids []int64, hashes [][]byte, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := m.nodes[tenantID+"\x00"+nodeID]
	if rows == nil {
		return false
	}
	for i := range uids {
		at, ok := rows[aliveKeyOf(uids[i], hashes[i])]
		if !ok || now.Sub(at) >= aliveRefreshEvery {
			return false
		}
	}
	return true
}

// record 记下一次写入里真被插入或刷新的行（at 取发起写入之前的时刻，不晚于库里的 now()），
// 顺手丢掉这个节点已过刷新点的旧行。
func (m *aliveMemo) record(tenantID, nodeID string, uids []int64, hashes [][]byte, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantID + "\x00" + nodeID
	rows := m.nodes[key]
	if rows == nil {
		rows = make(map[aliveMemoKey]time.Time, len(uids))
		m.nodes[key] = rows
	}
	for k, t := range rows {
		if at.Sub(t) >= aliveRefreshEvery {
			delete(rows, k)
			m.size--
		}
	}
	for i := range uids {
		k := aliveKeyOf(uids[i], hashes[i])
		if _, ok := rows[k]; !ok {
			if m.size >= aliveMemoMax {
				continue
			}
			m.size++
		}
		rows[k] = at
	}
	if len(rows) == 0 {
		delete(m.nodes, key)
	}
}

// aliveMemo 返回进程的在线上报备忘；没开节点缓存（非 aegis-node）时为 nil，每份都写。
func (s *Service) aliveMemo() *aliveMemo {
	if s.caches == nil {
		return nil
	}
	return s.caches.alive
}
