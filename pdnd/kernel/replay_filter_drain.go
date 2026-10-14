package kernel

import (
	"container/list"
	"net"
	"net/netip"
	"sync"
)

// drainRegistry 是全进程正在排空的连接（drainUntilPeerClose）的名册，按登记
// 先后排队，另按来源分队。
//
// 名额满时踢最老的、而不是拒新的：拒新的话，名额被未认证的一方占满之后（开
// 4096 条连接各发几十字节就挂着，对端内核活着就会回应 keepalive）所有认证失败
// 都退回「读到固定字节数就关」，接收缓冲里还有没读的字节，关闭发的是 RST——正是
// 排空要消掉的特征。踢最老的则：新来的探测照样排空；被踢的那条在 Read 上阻塞、
// 早已读空，关它发的是 FIN；占名额的一方得不停开新连接才能占住，而它的新连接
// 也只是把它自己更早的连接挤掉（同源上限先于全局上限生效）。
type drainRegistry struct {
	mu       sync.Mutex
	all      list.List             // *drainEntry，最老的在前
	bySource map[string]*list.List // 来源 → *drainEntry，最老的在前
}

type drainEntry struct {
	conn     net.Conn
	source   string
	all      *list.Element
	bySource *list.Element
	evicted  bool // 已被踢出名册（由踢的一方关连接、减计数）
}

var drains = &drainRegistry{bySource: make(map[string]*list.List)}

// register 登记一条要排空的连接。名额满（全局或同源）时先踢最老的，被踢的连接
// 在锁外关闭。上限为 0 或负数时不排空（返回 false，调用方随即关闭）。
func (r *drainRegistry) register(conn net.Conn) (*drainEntry, bool) {
	maxAll, maxSource := drainMaxConcurrent.Load(), drainMaxPerSource.Load()
	if maxAll <= 0 || maxSource <= 0 {
		return nil, false
	}
	entry := &drainEntry{conn: conn, source: drainSource(conn.RemoteAddr())}
	var victims []net.Conn
	r.mu.Lock()
	queue := r.bySource[entry.source]
	if queue == nil {
		queue = list.New()
		r.bySource[entry.source] = queue
	}
	for int64(queue.Len()) >= maxSource {
		victims = append(victims, r.evictLocked(queue.Front().Value.(*drainEntry)))
	}
	for int64(r.all.Len()) >= maxAll {
		victims = append(victims, r.evictLocked(r.all.Front().Value.(*drainEntry)))
	}
	// 被踢的若是本来源最后一条，evictLocked 会删掉本来源的队列；重新取。
	if queue = r.bySource[entry.source]; queue == nil {
		queue = list.New()
		r.bySource[entry.source] = queue
	}
	entry.all = r.all.PushBack(entry)
	entry.bySource = queue.PushBack(entry)
	drainActive.Add(1)
	r.mu.Unlock()
	for _, c := range victims {
		_ = c.Close()
	}
	return entry, true
}

// unregister 在排空结束时注销；已被踢的不重复减计数。
func (r *drainRegistry) unregister(entry *drainEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry.evicted {
		return
	}
	r.removeLocked(entry)
}

// evictLocked 把 entry 踢出名册，返回要在锁外关闭的连接。
func (r *drainRegistry) evictLocked(entry *drainEntry) net.Conn {
	entry.evicted = true
	r.removeLocked(entry)
	return entry.conn
}

func (r *drainRegistry) removeLocked(entry *drainEntry) {
	r.all.Remove(entry.all)
	if queue := r.bySource[entry.source]; queue != nil {
		queue.Remove(entry.bySource)
		if queue.Len() == 0 {
			delete(r.bySource, entry.source)
		}
	}
	drainActive.Add(-1)
}

// drainSource 是排空按来源计数的键：IPv4 取整个地址，IPv6 取 /64（一个用户
// 通常分到整段 /64，按单个地址计数挡不住）。取不到地址的归到同一个空键。
func drainSource(addr net.Addr) string {
	var ip netip.Addr
	switch a := addr.(type) {
	case *net.TCPAddr:
		ip, _ = netip.AddrFromSlice(a.IP)
	case nil:
		return ""
	default:
		if ap, err := netip.ParseAddrPort(a.String()); err == nil {
			ip = ap.Addr()
		}
	}
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return ""
	case ip.Is4():
		return ip.String()
	default:
		prefix, _ := ip.Prefix(64)
		return prefix.String()
	}
}
