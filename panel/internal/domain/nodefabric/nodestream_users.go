package nodefabric

import (
	"encoding/json"
	"sync"
	"time"
)

// 用户名单的推送：版本历史、共享编码、增量与「REST 拉过别的版本」的标脏。
//
// 一份名单的版本（UserSetVersion）只由内容决定，所以「同 (池, 版本)」就是「同版本」：
// 全量载荷按版本只编码一次，所有连接共用同一份字节；增量按 (起点版本, 终点版本)
// 只算一次。5k 实测里租户级变更给 300 个节点各推一份 0.44 MB 的全量（每次约 130 MB、
// 推送 6.5 秒）；节点重启后 2000 条新连接各编码一份全量，进程峰值 445 MB，超过 aegis-node
// 256M 的内存上限。

const (
	// userHistoryMax 是记住的名单版本数。增量要从连接手上的版本算起，所以要留着
	// 最近几版的原文；超出的最旧一版丢掉，停在那一版的连接改推全量。
	userHistoryMax = 8
	// userDeltaCacheMax 是记住的已编码增量份数（按起点、终点版本）。
	userDeltaCacheMax = 32
	// userEncodeConcurrency 是同时编码全量名单的上限。同一版本只编码一次，
	// 这个上限管的是「几个池同时出新版本」时 CPU 与临时内存的峰值。
	userEncodeConcurrency = 2
)

// userVersionEntry 是一个版本的名单原文，及按需编码一次的全量消息。
type userVersionEntry struct {
	version string
	users   []ProxyUser // 只读，可能与缓存共享

	encodeOnce sync.Once
	full       []byte
	fullErr    error
	used       uint64
}

// userDeltaEntry 是一对版本之间按需算一次的增量消息；msg 为 nil 表示这一对该推全量。
type userDeltaEntry struct {
	once sync.Once
	msg  []byte
	used uint64
}

type userPayloads struct {
	mu     sync.Mutex
	clock  uint64
	byVer  map[string]*userVersionEntry
	deltas map[string]*userDeltaEntry
	encode chan struct{}
}

func newUserPayloads() *userPayloads {
	return &userPayloads{
		byVer:  make(map[string]*userVersionEntry),
		deltas: make(map[string]*userDeltaEntry),
		encode: make(chan struct{}, userEncodeConcurrency),
	}
}

// remember 登记一个版本的名单原文，返回它的条目。已登记过的版本沿用原条目
// （连同已编码的全量），不重复编码。
func (p *userPayloads) remember(version string, users []ProxyUser) *userVersionEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clock++
	if e, ok := p.byVer[version]; ok {
		e.used = p.clock
		return e
	}
	if len(p.byVer) >= userHistoryMax {
		evictOldest(p.byVer, func(e *userVersionEntry) uint64 { return e.used })
	}
	e := &userVersionEntry{version: version, users: users, used: p.clock}
	p.byVer[version] = e
	return e
}

func (p *userPayloads) lookup(version string) *userVersionEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byVer[version]
	if e != nil {
		p.clock++
		e.used = p.clock
	}
	return e
}

// fullMessage 返回这个版本的全量消息，所有连接共用同一份字节。
// 并发的首次调用只编码一次，其余等它；同时编码的版本数受 userEncodeConcurrency 限制。
func (p *userPayloads) fullMessage(e *userVersionEntry) ([]byte, error) {
	e.encodeOnce.Do(func() {
		p.encode <- struct{}{}
		defer func() { <-p.encode }()
		data, err := json.Marshal(SyncUsersPayload{Users: e.users, Version: e.version})
		if err != nil {
			e.fullErr = err
			return
		}
		e.full = encodeStreamMessage(EventSyncUsers, data, time.Now().UnixMilli())
	})
	return e.full, e.fullErr
}

// deltaMessage 返回从 from 到 to 的增量消息；ok=false 表示这一对推全量
// （起点版本已不在历史里，或增量条数不比全量少）。
func (p *userPayloads) deltaMessage(from string, to *userVersionEntry) ([]byte, bool) {
	old := p.lookup(from)
	if old == nil {
		return nil, false
	}
	key := from + "\x00" + to.version
	p.mu.Lock()
	p.clock++
	d, ok := p.deltas[key]
	if !ok {
		if len(p.deltas) >= userDeltaCacheMax {
			evictOldest(p.deltas, func(e *userDeltaEntry) uint64 { return e.used })
		}
		d = &userDeltaEntry{}
		p.deltas[key] = d
	}
	d.used = p.clock
	p.mu.Unlock()

	d.once.Do(func() {
		delta := DiffUsers(old.users, to.users)
		// 增量比全量还大就没必要发增量——批量改动时常有这种情况。
		// 判据用条数而不是字节数：字节数要先序列化两遍才知道。
		if len(delta.Added)+len(delta.Removed) >= len(to.users) {
			return
		}
		data, err := json.Marshal(SyncUserDeltaPayload{Delta: delta, FromVersion: from, ToVersion: to.version})
		if err != nil {
			return
		}
		d.msg = encodeStreamMessage(EventSyncUserDelta, data, time.Now().UnixMilli())
	})
	return d.msg, d.msg != nil
}

func evictOldest[V any](m map[string]V, used func(V) uint64) {
	var oldestKey string
	var oldest uint64
	first := true
	for k, v := range m {
		if u := used(v); first || u < oldest {
			oldestKey, oldest, first = k, u, false
		}
	}
	if !first {
		delete(m, oldestKey)
	}
}

// PushUsers 把用户名单推给这个节点的所有连接。
//
// 每条连接各自判断：手上已是这一版就跳过；手上的版本还在历史里、且这个节点
// 没有经 REST 拿过别的版本，就发增量；否则发全量。这是长连接相比轮询的关键
// 优势——服务端知道每条连接处在哪一版。version 由调用方给（缓存里算好的直接用）。
func (h *StreamHub) PushUsers(tenantID, nodeID string, users []ProxyUser, version string) {
	entry := h.users.remember(version, users)
	pulling := h.pullsInFlight(tenantID, nodeID)
	for _, c := range h.targets(tenantID, nodeID) {
		h.pushUsersTo(c, entry, pulling)
	}
}

// PushInitialUsers 是一条新连接的第一次推送。
//
// 节点重连时带上自己手上的版本（StreamUsersVersionHeader），和当前版一致就不推
// 全量——节点网关重启后几百条连接同时回来，原先每条都要推一份几百 KB 的全量。
// 跳过时仍把连接标脏：节点报的版本可能只是它记着的流版本，而它实际装着的名单
// 来自 REST（pdnd 的 REST 拉取不更新流版本），下一次变更给它推全量，不推增量，
// 免得把增量打在一份不一样的名单上。
func (h *StreamHub) PushInitialUsers(c *StreamConn, users []ProxyUser, version, claimed string) {
	entry := h.users.remember(version, users)
	if claimed != "" && claimed == version {
		c.mu.Lock()
		c.version, c.stale = version, true
		c.mu.Unlock()
		return
	}
	h.pushUsersTo(c, entry, h.pullsInFlight(c.TenantID, c.NodeID))
}

// pushUsersTo 在一条连接上完成「选全量或增量 → 入队 → 记版本」。
// pulling 表示推送开始时这个节点有 REST 拉用户的请求在途。
func (h *StreamHub) pushUsersTo(c *StreamConn, entry *userVersionEntry, pulling bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	from := c.version
	if from == entry.version {
		return // 这条连接已经是最新的
	}
	var msg []byte
	// 增量只在「节点手上一定是 from 那一版」时才能发：
	//   - from 为空是新连接，节点手上是什么面板不知道；
	//   - stale：节点经 REST 拿过别的版本，手上的名单已不是 from；
	//   - pulling：REST 拉取正在途中，它送达的名单可能比这条增量晚到、又早于它被应用。
	// pdnd 收到增量时只核对 from_version 与自己记着的流版本，不核对内核里实际的名单，
	// 打错了会把一批本该删掉的用户留在放行名单里，所以面板这一侧必须先排除这些情形。
	if from != "" && !c.stale && !pulling {
		if delta, ok := h.users.deltaMessage(from, entry); ok {
			msg = delta
		}
	}
	full := msg == nil
	if full {
		var err error
		if msg, err = h.users.fullMessage(entry); err != nil {
			return
		}
	}
	if !h.pushOne(c, msg) {
		return
	}
	c.version = entry.version
	if full {
		c.stale = false
	}
}

// BeginUsersPull 记下一次 REST 拉用户（UniProxy /user）开始，返回的 done 在响应
// 写出之前调用：delivered 表示回了 200（节点会装上 version 这一版），304 与失败
// 传 false。
//
// 节点经 REST 装上的名单与它的事件流版本是两条路：拉取在途时推增量，或拉取送去
// 别的版本之后再推增量，都可能把增量打在不对应的名单上。所以：
//   - 在途期间，这个节点的推送一律走全量；
//   - 送出的版本与某条连接记着的版本不同，这条连接标脏，下一次推全量。
func (h *StreamHub) BeginUsersPull(tenantID, nodeID string) (done func(version string, delivered bool)) {
	key := streamKey(tenantID, nodeID)
	h.mu.Lock()
	h.pulls[key]++
	h.mu.Unlock()
	var once sync.Once
	return func(version string, delivered bool) {
		once.Do(func() {
			// 先标脏、后减在途计数：两步之间开始的推送看到的仍是「在途」，走全量。
			if delivered {
				for _, c := range h.targets(tenantID, nodeID) {
					c.mu.Lock()
					if c.version != version {
						c.stale = true
					}
					c.mu.Unlock()
				}
			}
			h.mu.Lock()
			if h.pulls[key]--; h.pulls[key] <= 0 {
				delete(h.pulls, key)
			}
			h.mu.Unlock()
		})
	}
}

func (h *StreamHub) pullsInFlight(tenantID, nodeID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pulls[streamKey(tenantID, nodeID)] > 0
}

// StreamUsersVersionHeader 是节点建事件流时报告「我手上的用户名单是哪一版」的请求头，
// 值与 UniProxy /user 的 ETag 同源（UserSetVersion）。与当前版一致时面板不推首个全量。
// 不带或写错都只是退回推全量，不影响正确性。
const StreamUsersVersionHeader = "X-Users-Version"
