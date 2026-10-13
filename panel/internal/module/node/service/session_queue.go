package service

// 一条会话：有界发送队列（配置只留最新一份、每个节点的名单只留最新一份）、结束与收尾。

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
)

// outItem 是队列里的一条；配置与名单被新的取代时原地换掉内容（位置不变）。
type outItem struct {
	msg Outbound
	// cfg 是 OutConfig 出队时记为「已发」的节点 → 摘要
	cfg map[uuid.UUID]string
	// terminal 为真：发出后会话以 termErr 结束
	terminal bool
	// plain 为真：普通消息（Welcome、TrafficAck、Certificate），计入队列上限
	plain bool
}

// Session 是一台服务器的一条在途控制流。传输层：Next 取要发的消息，Handle* 交收到的消息，结束时 Close。
type Session struct {
	m        *Sessions
	serverID uuid.UUID
	id       string
	features []string
	delta    bool

	mu sync.Mutex
	// 发送队列；plain 是其中普通消息的条数（配置与名单按节点合并，条数以节点数为界，不计入上限）
	items   []*outItem
	plain   int
	cfgItem *outItem
	rosters map[uuid.UUID]*outItem
	wake    chan struct{}
	// termErr 非 nil：已排上终止消息，不再收新消息
	termErr error
	// err 非 nil：会话已结束；done 随之关闭
	err  error
	done chan struct{}

	// 下发状态（session_roster.go）
	cfgSeq, usersSeq uint64
	// cfgWant 是节点在收完已排队配置后会有的配置（节点 id → 摘要）；cfgSent 是已发出（或 Hello 时已一致）的
	cfgWant, cfgSent map[uuid.UUID]string
	nodes            map[uuid.UUID]*rosterState
}

func newSession(m *Sessions, serverID uuid.UUID, id string, h Hello) *Session {
	s := &Session{
		m: m, serverID: serverID, id: id,
		rosters: map[uuid.UUID]*outItem{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		cfgWant: map[uuid.UUID]string{},
		cfgSent: map[uuid.UUID]string{},
		nodes:   map[uuid.UUID]*rosterState{},
	}
	for _, f := range panelFeatures {
		if slices.Contains(h.Features, f) {
			s.features = append(s.features, f)
		}
	}
	s.delta = slices.Contains(s.features, FeatureUsersDelta)
	for id, sha := range h.AppliedConfigs {
		s.cfgWant[id] = sha
		s.cfgSent[id] = sha
	}
	for id, v := range h.AppliedUsers {
		s.nodes[id] = &rosterState{latestVersion: v, confirmed: v}
	}
	return s
}

// ID 是会话 id（Welcome.session_id，日志用）。
func (s *Session) ID() string { return s.id }

// ServerID 是这条流的服务器 id。
func (s *Session) ServerID() uuid.UUID { return s.serverID }

// Done 在会话结束时关闭。
func (s *Session) Done() <-chan struct{} { return s.done }

// Err 是会话结束的原因；没结束时为 nil。
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *Session) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// accepting 报告还收不收新消息（调用方持有 s.mu）。
func (s *Session) acceptingLocked() bool { return s.err == nil && s.termErr == nil }

// pushLocked 追加一条；普通消息超过上限就断流，让节点重连后重新对齐（调用方持有 s.mu）。返回 false 表示没收下。
func (s *Session) pushLocked(it *outItem) bool {
	if !s.acceptingLocked() {
		return false
	}
	if it.plain {
		if s.plain >= s.m.opt.QueueLimit {
			s.closeLocked(ErrSessionOverflow)
			return false
		}
		s.plain++
	}
	s.items = append(s.items, it)
	s.signal()
	return true
}

// enqueue 排一条普通消息（Welcome、TrafficAck、Certificate）。
func (s *Session) enqueue(msg Outbound) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushLocked(&outItem{msg: msg, plain: true})
}

// putConfigLocked 排一份全量配置；队列里还有没发出的配置就原地换掉。
func (s *Session) putConfigLocked(msg Outbound, cfg map[uuid.UUID]string) {
	if !s.acceptingLocked() {
		return
	}
	if s.cfgItem != nil {
		s.cfgItem.msg, s.cfgItem.cfg = msg, cfg
		s.signal()
		return
	}
	it := &outItem{msg: msg, cfg: cfg}
	if s.pushLocked(it) {
		s.cfgItem = it
	}
}

// putRosterLocked 排一个节点的名单（全量或增量）；队列里这个节点还有没发出的名单就原地换掉。
func (s *Session) putRosterLocked(nodeID uuid.UUID, msg Outbound) {
	if !s.acceptingLocked() {
		return
	}
	if it := s.rosters[nodeID]; it != nil {
		it.msg = msg
		s.signal()
		return
	}
	it := &outItem{msg: msg}
	if s.pushLocked(it) {
		s.rosters[nodeID] = it
	}
}

// dropRosterLocked 撤掉队列里这个节点还没发出的名单。
func (s *Session) dropRosterLocked(nodeID uuid.UUID) {
	it := s.rosters[nodeID]
	if it == nil {
		return
	}
	delete(s.rosters, nodeID)
	if i := slices.Index(s.items, it); i >= 0 {
		s.items = slices.Delete(s.items, i, i+1)
	}
}

// terminate 清空队列、只排一条终止消息（Tombstone 或 reconnect），发出后以 err 结束；
// TerminateGrace 内没发出去（节点不读）就强关。已在结束中的会话不再处理。
func (s *Session) terminate(msg Outbound, err error) {
	s.mu.Lock()
	if !s.acceptingLocked() {
		s.mu.Unlock()
		return
	}
	s.items = []*outItem{{msg: msg, terminal: true}}
	s.plain = 0
	s.cfgItem = nil
	clear(s.rosters)
	s.termErr = err
	s.signal()
	s.mu.Unlock()
	time.AfterFunc(s.m.opt.TerminateGrace, func() { s.closeWith(err) })
}

// closeWith 结束会话（只认第一次的原因），唤醒阻塞在 Next 上的发送循环。不动登记表。
func (s *Session) closeWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked(err)
}

func (s *Session) closeLocked(err error) {
	if s.err != nil {
		return
	}
	s.err = err
	s.items = nil
	s.plain = 0
	s.cfgItem = nil
	clear(s.rosters)
	close(s.done)
}

// Close 由传输层在流结束时调（defer）：结束会话并移出登记（只移除自己）。
func (s *Session) Close(err error) {
	if err == nil {
		err = ErrSessionPeerGone
	}
	s.closeWith(err)
	s.m.unregister(s)
}

// Next 阻塞到有消息可发：返回下一条；会话已结束时返回结束原因；ctx 结束返回 ctx.Err()。
// 只能由一个发送循环调用。出队即记为「已发出」（名单成员判定、配置回执核对按它）。
func (s *Session) Next(ctx context.Context) (Outbound, error) {
	for {
		s.mu.Lock()
		if s.err != nil {
			err := s.err
			s.mu.Unlock()
			return Outbound{}, err
		}
		if len(s.items) > 0 {
			it := s.items[0]
			s.items[0] = nil
			s.items = s.items[1:]
			s.onDequeueLocked(it)
			s.mu.Unlock()
			return it.msg, nil
		}
		s.mu.Unlock()
		select {
		case <-s.wake:
		case <-s.done:
		case <-ctx.Done():
			return Outbound{}, ctx.Err()
		}
	}
}

// onDequeueLocked 记下出队的消息；终止消息出队后会话以 termErr 结束（这条照样返回给发送循环发出去）。
func (s *Session) onDequeueLocked(it *outItem) {
	switch {
	case it.terminal:
		s.closeLocked(s.termErr)
	case it.plain:
		s.plain--
	case it.msg.Kind == OutConfig:
		if s.cfgItem == it {
			s.cfgItem = nil
		}
		s.cfgSent = it.cfg
	case it.msg.Roster != nil:
		id := it.msg.Roster.NodeID
		if s.rosters[id] == it {
			delete(s.rosters, id)
		}
		if st := s.nodes[id]; st != nil {
			st.sent = it.msg.Roster
		}
	}
}
