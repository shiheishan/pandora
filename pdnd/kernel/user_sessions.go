package kernel

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 按用户的在途连接表与原子流量计数，每个适配器一份（userSessions）。
//
// 解决两件事：
//
//  1. 删用户即断线。原先适配器只记 active（连接集合，不知道属于谁），DelUsers
//     只删用户表，已有连接、mux / QUIC 会话照常可用——用户到期、被封后长连接
//     能一直用下去。现在认证通过后按 (用户 ID, 凭据) 登记，被移出名单时在锁外
//     关掉他的全部连接。
//  2. 流量按周期计入。原先 TCP 要到连接结束才把字节数加进 traffic 表，长连接
//     跨多少个上报周期都不计，进程被强杀就丢。现在转发每搬一块就原子累加到
//     用户的计数器上，SnapshotTraffic 随时取走增量。
//
// 认证与登记之间的竞态：连接先取 epoch（读请求之前），认证通过后带着它登记；
// 这期间该用户若被撤销过（revoked[key] > epoch），登记失败。撤销先从适配器的
// 用户表删、再到这里记号并关连接，所以「查到用户之后、登记之前」被删的连接
// 要么登记被拒，要么登记成功后被撤销关掉，不会漏网。

// sessionKey 是面板用户 ID。按 ID 而不是按凭据记：各协议存的凭据形态不一
// （口令哈希、派生密钥、槽位），按 ID 撤销最不容易漏。代价是重置口令（同 ID
// 换 UUID，节点端表现为先加新、后删旧）时新凭据的连接也会被断一次，客户端
// 重连即可——宁可多断，不能漏断。
type sessionKey = int64

type userSessions struct {
	mu      sync.Mutex
	seq     uint64
	conns   map[sessionKey]map[*userSession]struct{}
	traffic map[int64]*userCounter
	revoked map[sessionKey]revocation
	live    int
}

type revocation struct {
	seq uint64
	at  time.Time
}

// userCounter 是一个用户的流量增量，转发直接对它原子累加。refs 是在用它的
// 连接数（mu 保护），为 0 且增量为 0 时快照顺手删掉，map 不会只增不减。
type userCounter struct {
	up, down atomic.Int64
	refs     int
}

// userSession 是一条已登记的连接（或一个 mux / QUIC 会话）。
type userSession struct {
	owner   *userSessions
	key     sessionKey
	counter *userCounter
	closers []io.Closer
	once    sync.Once
}

// errSessionRevoked：认证之后、登记之前，这个用户被移出了名单。
var errSessionRevoked = markConnError(connErrAuth, errors.New("用户已被移出名单"))

// revokedKeep 是撤销记号的保留时长：只用来挡「认证中」的连接，握手有 10 秒级
// 的截止时间，留 2 分钟足够。
const revokedKeep = 2 * time.Minute

// epoch 在读请求之前调用，登记时原样带回。
func (s *userSessions) epoch() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// open 登记一条已认证的连接，closers 是被踢时要关的对象（通常是客户端连接）。
// 返回 nil 表示凭据在 epoch 之后被撤销，调用方应断开。
func (s *userSessions) open(user core.User, epoch uint64, closers ...io.Closer) *userSession {
	key := user.ID
	s.mu.Lock()
	if r, ok := s.revoked[key]; ok && r.seq > epoch {
		s.mu.Unlock()
		return nil
	}
	if s.conns == nil {
		s.conns = make(map[sessionKey]map[*userSession]struct{})
	}
	set := s.conns[key]
	if set == nil {
		set = make(map[*userSession]struct{})
		s.conns[key] = set
	}
	sess := &userSession{owner: s, key: key, counter: s.counterLocked(user.ID), closers: closers}
	sess.counter.refs++
	set[sess] = struct{}{}
	s.live++
	s.mu.Unlock()
	return sess
}

func (s *userSessions) counterLocked(id int64) *userCounter {
	if s.traffic == nil {
		s.traffic = make(map[int64]*userCounter)
	}
	c := s.traffic[id]
	if c == nil {
		c = &userCounter{}
		s.traffic[id] = c
	}
	return c
}

// close 注销。被踢的连接随后照样调它，只做一次。
func (sess *userSession) close() {
	if sess == nil {
		return
	}
	sess.once.Do(func() {
		s := sess.owner
		s.mu.Lock()
		if set := s.conns[sess.key]; set != nil {
			if _, ok := set[sess]; ok {
				delete(set, sess)
				s.live--
				if len(set) == 0 {
					delete(s.conns, sess.key)
				}
			}
		}
		sess.counter.refs--
		s.mu.Unlock()
	})
}

// up / down 是转发用的计数器。
func (sess *userSession) up() *atomic.Int64   { return &sess.counter.up }
func (sess *userSession) down() *atomic.Int64 { return &sess.counter.down }

// relay 在这条会话上做双向转发，字节数随搬随记到用户计数器上。opt 里的
// Up / Down 由这里填。
func (sess *userSession) relay(client, upstream core.RelayStream, opt core.RelayOptions) {
	opt.Up, opt.Down = sess.up(), sess.down()
	core.Relay(client, upstream, opt)
}

// retain 取一个用户的计数器并占住它（快照不会删正在用的计数器），用完 release。
// 给 mux 子流这类不单独登记会话、但要随搬随计的转发。
func (s *userSessions) retain(id int64) *userCounter {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counterLocked(id)
	c.refs++
	return c
}

func (s *userSessions) release(c *userCounter) {
	s.mu.Lock()
	c.refs--
	s.mu.Unlock()
}

// add 记一笔不走 Relay 的流量（UDP 逐包、mux 子流）。
func (s *userSessions) add(id int64, up, down int64) {
	if up == 0 && down == 0 {
		return
	}
	s.mu.Lock()
	c := s.counterLocked(id)
	s.mu.Unlock()
	if up != 0 {
		c.up.Add(up)
	}
	if down != 0 {
		c.down.Add(down)
	}
}

// peek 读一个用户当前未取走的增量，不清零（测试与诊断用）。
func (s *userSessions) peek(id int64) core.UserTraffic {
	s.mu.Lock()
	c := s.traffic[id]
	s.mu.Unlock()
	if c == nil {
		return core.UserTraffic{ID: id}
	}
	return core.UserTraffic{ID: id, Upload: c.up.Load(), Download: c.down.Load()}
}

// snapshot 取走全部用户的增量。
func (s *userSessions) snapshot() []core.UserTraffic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(s.traffic))
	for id, c := range s.traffic {
		up, down := c.up.Swap(0), c.down.Swap(0)
		if up != 0 || down != 0 {
			out = append(out, core.UserTraffic{ID: id, Upload: up, Download: down})
		}
		if c.refs == 0 {
			// 没有连接在用了：这一刻之后不会再有人往里加（add 在锁内取计数器）。
			delete(s.traffic, id)
		}
	}
	return out
}

// revoke 撤销这些用户并关掉他们的全部连接（锁外关）。ids 是适配器刚从自己用户表
// 里删掉的用户 ID。
func (s *userSessions) revoke(ids []int64) {
	if len(ids) == 0 {
		return
	}
	var victims []*userSession
	now := time.Now()
	s.mu.Lock()
	if s.revoked == nil {
		s.revoked = make(map[sessionKey]revocation)
	}
	for _, key := range ids {
		s.seq++
		s.revoked[key] = revocation{seq: s.seq, at: now}
		for sess := range s.conns[key] {
			victims = append(victims, sess)
		}
		if set := s.conns[key]; set != nil {
			s.live -= len(set)
			delete(s.conns, key)
		}
	}
	if len(s.revoked) > 1024 {
		for key, r := range s.revoked {
			if now.Sub(r.at) > revokedKeep {
				delete(s.revoked, key)
			}
		}
	}
	s.mu.Unlock()
	for _, sess := range victims {
		for _, c := range sess.closers {
			if c != nil {
				closeAbruptly(c)
			}
		}
	}
}

// closeAbruptly 是踢人用的关闭：带安全层的连接（TLS / REALITY，有 NetConn）先关
// 底层 TCP 再关外层。外层 Close 会先写一条 close_notify（带 5 秒写截止）：对端不读
// 时每条连接卡满 5 秒、逐条串行；Vision 直通之后这条加密告警还会落进裸流，被对端
// 当成内层数据。被踢的连接不需要体面收尾。
func closeAbruptly(c io.Closer) {
	if nc, ok := c.(interface{ NetConn() net.Conn }); ok {
		if under := nc.NetConn(); under != nil {
			_ = under.Close()
		}
	}
	_ = c.Close()
}

// liveCount 是已登记、还没结束的会话数（关停排空等它归零）。
func (s *userSessions) liveCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

// sessionTracker 由有用户连接表的适配器实现：关停排空看它的在途会话数，
// 测试据此断言连接已释放。
type sessionTracker interface {
	userSessionTable() *userSessions
}

func (a *vlessAdapter) userSessionTable() *userSessions       { return &a.sessions }
func (a *vmessAdapter) userSessionTable() *userSessions       { return &a.sessions }
func (a *trojanAdapter) userSessionTable() *userSessions      { return &a.sessions }
func (a *shadowsocksAdapter) userSessionTable() *userSessions { return &a.sessions }
func (a *ss2022Adapter) userSessionTable() *userSessions      { return &a.sessions }
func (a *proxyAdapter) userSessionTable() *userSessions       { return &a.sessions }
func (a *naiveAdapter) userSessionTable() *userSessions       { return &a.sessions }
func (a *anyTLSAdapter) userSessionTable() *userSessions      { return &a.sessions }
func (a *hysteria2Adapter) userSessionTable() *userSessions   { return &a.sessions }
func (a *tuicAdapter) userSessionTable() *userSessions        { return &a.sessions }
func (a *juicityAdapter) userSessionTable() *userSessions     { return &a.sessions }

// liveSessionsOf 是适配器当前已登记的在途会话数；没有连接表的适配器返回 0。
func liveSessionsOf(a Adapter) int {
	switch v := a.(type) {
	case sessionTracker:
		return v.userSessionTable().liveCount()
	case interface{ innerAdapter() Adapter }:
		return liveSessionsOf(v.innerAdapter())
	}
	return 0
}
