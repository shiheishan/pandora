// Package counter 提供按用户的流量统计、用户表与在线 IP 记录。
//
// 独立成包是因为不止 sing-box 内核需要它：Mieru 有自己的监听器，
// xray-core 也会有，但「谁用了多少流量、谁在线」的语义对所有内核都一样。
// 这些逻辑只该有一份实现 —— 计费出错是最难发现也最难解释的一类故障。
package counter

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/aegispanel/nodeagent/core"
)

//------------------------------------------------------------------------------
// 流量统计
//------------------------------------------------------------------------------

// UserStats 是单个用户的累计流量。
//
// 用原子操作而不是加锁：统计写入发生在每一次数据拷贝上，是全链路最热的路径，
// 一把互斥锁会成为高并发下的瓶颈；而读取（上报时）对精确性的要求本就是
// 「最终一致」，原子操作完全够用。
type UserStats struct {
	up   atomic.Int64
	down atomic.Int64
}

func (s *UserStats) AddUp(n int64)   { s.up.Add(n) }
func (s *UserStats) AddDown(n int64) { s.down.Add(n) }

// Registry 按用户维护流量计数。
type Registry struct {
	mu sync.RWMutex
	// 以面板下发的整数 ID 为键：上报时要按它回传，用 UUID 还得再映射一次
	byUser map[int64]*UserStats
}

func NewRegistry() *Registry {
	return &Registry{byUser: make(map[int64]*UserStats)}
}

func (s *Registry) Get(id int64) *UserStats {
	s.mu.RLock()
	st, ok := s.byUser[id]
	s.mu.RUnlock()
	if ok {
		return st
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 双重检查：并发首次访问同一用户时不能覆盖已建好的计数器，
	// 否则那一瞬间已记录的流量会丢失
	if st, ok = s.byUser[id]; ok {
		return st
	}
	st = &UserStats{}
	s.byUser[id] = st
	return st
}

// Drain 取出全部增量并清零。
//
// 取出即清零是刻意的：拆成「读」和「清零」两步的话，
// 两步之间产生的流量会被永久丢弃，长期运行下累积的误差相当可观。
func (s *Registry) Drain() []core.UserTraffic {
	s.mu.RLock()
	ids := make([]int64, 0, len(s.byUser))
	sts := make([]*UserStats, 0, len(s.byUser))
	for id, st := range s.byUser {
		ids = append(ids, id)
		sts = append(sts, st)
	}
	s.mu.RUnlock()

	out := make([]core.UserTraffic, 0, len(ids))
	for i, st := range sts {
		up := st.up.Swap(0)
		down := st.down.Swap(0)
		if up == 0 && down == 0 {
			continue
		}
		out = append(out, core.UserTraffic{ID: ids[i], Upload: up, Download: down})
	}
	return out
}

func (s *Registry) Remove(id int64) {
	s.mu.Lock()
	delete(s.byUser, id)
	s.mu.Unlock()
}

//------------------------------------------------------------------------------
// 计数连接
//------------------------------------------------------------------------------

// Conn 在读写路径上累加流量。
//
// 方向以节点为参照：客户端发来的（我们 Read 到的）算上行，
// 我们发回去的算下行。与面板 push 接口的 [upload, download] 语义一致。
type Conn struct {
	net.Conn
	st *UserStats
}

func NewConn(conn net.Conn, st *UserStats) *Conn {
	return &Conn{Conn: conn, st: st}
}

func (c *Conn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.st.up.Add(int64(n))
	}
	return n, err
}

func (c *Conn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.st.down.Add(int64(n))
	}
	return n, err
}

// Upstream 让 sing 的 bufio 优化链能穿透这层包装。
// 不实现它的话，splice / sendfile 之类的零拷贝路径会被这层挡掉，
// 转发性能会有可观的下降。
func (c *Conn) Upstream() any { return c.Conn }

// PacketConn 对 UDP 做同样的统计。
// UDP 必须单独包一层：TCP 的 net.Conn 与 sing 的 N.PacketConn 是两套接口，
// 只包 TCP 的话，走 UDP 的流量（QUIC、游戏、DNS）会完全不计费。
type PacketConn struct {
	N.PacketConn
	st *UserStats
}

func NewPacketConn(conn N.PacketConn, st *UserStats) *PacketConn {
	return &PacketConn{PacketConn: conn, st: st}
}

func (c *PacketConn) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	before := buffer.Len()
	dest, err := c.PacketConn.ReadPacket(buffer)
	if n := buffer.Len() - before; n > 0 {
		c.st.up.Add(int64(n))
	}
	return dest, err
}

func (c *PacketConn) WritePacket(buffer *buf.Buffer, destination M.Socksaddr) error {
	n := buffer.Len()
	err := c.PacketConn.WritePacket(buffer, destination)
	if err == nil && n > 0 {
		c.st.down.Add(int64(n))
	}
	return err
}

func (c *PacketConn) Upstream() any { return c.PacketConn }

//------------------------------------------------------------------------------
// 用户表
//------------------------------------------------------------------------------

// Table 维护 uuid → 用户 的映射，并保证顺序稳定。
//
// 顺序稳定很重要：sing-box 的 service.UpdateUsers 用切片下标作为用户索引，
// 索引与 UUID 的对应关系一旦在两次更新间发生变化，
// 正在传输的连接就会被归到别的用户名下，流量记到错误的账上。
type Table struct {
	mu    sync.RWMutex
	order []string             // uuid，按加入顺序
	users map[string]core.User // uuid -> user
}

func NewTable() *Table {
	return &Table{users: make(map[string]core.User)}
}

// Add 返回真正新增的用户（已存在的被忽略）。
func (t *Table) Add(users []core.User) []core.User {
	t.mu.Lock()
	defer t.mu.Unlock()
	added := make([]core.User, 0, len(users))
	for _, u := range users {
		if u.UUID == "" {
			continue
		}
		if _, exists := t.users[u.UUID]; exists {
			continue
		}
		t.users[u.UUID] = u
		t.order = append(t.order, u.UUID)
		added = append(added, u)
	}
	return added
}

// Del 返回真正移除的 uuid。
func (t *Table) Del(uuids []string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	gone := make([]string, 0, len(uuids))
	drop := make(map[string]bool, len(uuids))
	for _, id := range uuids {
		if _, exists := t.users[id]; exists {
			delete(t.users, id)
			drop[id] = true
			gone = append(gone, id)
		}
	}
	if len(drop) > 0 {
		kept := t.order[:0]
		for _, id := range t.order {
			if !drop[id] {
				kept = append(kept, id)
			}
		}
		t.order = kept
	}
	return gone
}

// Snapshot 返回当前用户的有序副本。
func (t *Table) Snapshot() []core.User {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]core.User, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, t.users[id])
	}
	return out
}

// ByIndex 返回下标对应的用户，供连接归属判定使用。
func (t *Table) ByIndex(i int) (core.User, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if i < 0 || i >= len(t.order) {
		return core.User{}, false
	}
	u, ok := t.users[t.order[i]]
	return u, ok
}

// ByUUID 按 UUID 查找。部分协议（AnyTLS、Mieru）在上下文里传的是
// 用户名字符串而不是索引，只能这样回查。
func (t *Table) ByUUID(id string) (core.User, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	u, ok := t.users[id]
	return u, ok
}

func (t *Table) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.order)
}

//------------------------------------------------------------------------------
// 在线 IP
//------------------------------------------------------------------------------

// OnlineTracker 记录各用户当前在线的来源 IP，用于设备数限制（XBD-008）。
//
// 用 IP 近似「设备」是这类系统的通行做法，因为代理协议里拿不到任何
// 稳定的设备标识。这个近似有两个方向的误差，都要清楚：
//
//   - NAT 后的一家人共用一个出口 IP，会被算成一台设备（放宽了）
//   - 手机在 Wi-Fi 与蜂窝之间切换会换 IP，一台设备算成两台（收紧了）
//
// 后者更影响正常用户，所以下面用「最后活跃时间 + TTL」而不是
// 「统计周期内出现过」来判断在线：切换网络后旧 IP 会自然过期，
// 而不是在整个周期里一直占着名额。
type OnlineTracker struct {
	mu  sync.Mutex
	ttl time.Duration
	// userID -> IP -> 最后活跃时间
	seen map[int64]map[string]time.Time
}

// onlineTTL 是一个 IP 多久没活动就算下线。
//
// 5 分钟是个折中：短于此，长连接空闲时会被误判掉线，用户换设备时
// 反而更容易撞上限制；长于此，换了网络的用户要等很久名额才释放。
const onlineTTL = 5 * time.Minute

func NewOnlineTracker() *OnlineTracker {
	return &OnlineTracker{ttl: onlineTTL, seen: make(map[int64]map[string]time.Time)}
}

// Admit 判断能否再接受一条来自该地址的连接，并在允许时登记。
//
// limit <= 0 表示不限制。
//
// 语义是「拒绝新设备」而不是「踢掉旧设备」：后者会让两台设备
// 互相顶下线，来回拉扯，用户看到的是两边都时断时续 ——
// 那比明确地连不上更难排查。
func (o *OnlineTracker) Admit(userID int64, addr net.Addr, limit int) bool {
	host := hostOf(addr)
	if host == "" {
		return true
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	m, ok := o.seen[userID]
	if !ok {
		m = make(map[string]time.Time)
		o.seen[userID] = m
	}
	now := time.Now()

	// 已在线的地址直接放行并续期。这一步必须在限额判断之前 ——
	// 否则达到上限后，已连接设备的新连接（比如浏览器再开一个标签）
	// 也会被拒，表现成「用着用着突然断了」。
	if _, exists := m[host]; exists {
		m[host] = now
		return true
	}

	if limit > 0 {
		// 顺带清掉过期的，省掉一个专门的清理协程
		active := 0
		for ip, last := range m {
			if now.Sub(last) > o.ttl {
				delete(m, ip)
				continue
			}
			active++
		}
		if active >= limit {
			return false
		}
	}

	m[host] = now
	return true
}

// Mark 登记一次活动，不做限制判断。
func (o *OnlineTracker) Mark(userID int64, addr net.Addr) {
	o.Admit(userID, addr, 0)
}

// Snapshot 返回当前在线的 IP，供上报使用。
//
// 与早先的 Drain 不同，这里不清空 —— 清空会让「在线」退化成
// 「上报周期内出现过」，那正是设备限制误判的根源。过期由 TTL 负责。
func (o *OnlineTracker) Snapshot() map[int64][]string {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := time.Now()
	out := make(map[int64][]string, len(o.seen))
	for id, ips := range o.seen {
		list := make([]string, 0, len(ips))
		for ip, last := range ips {
			if now.Sub(last) > o.ttl {
				delete(ips, ip)
				continue
			}
			list = append(list, ip)
		}
		if len(list) == 0 {
			// 没有在线 IP 的用户不必出现在上报里，也顺手回收这一层 map
			delete(o.seen, id)
			continue
		}
		out[id] = list
	}
	return out
}

func hostOf(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
