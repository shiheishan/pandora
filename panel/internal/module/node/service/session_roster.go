package service

// 会话的下发状态：配置与名单的对齐、增量与全量的选择、名单成员判定。

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
)

// rosterState 是一个节点的名单下发状态。
type rosterState struct {
	// latestVersion 是节点在收完已排队消息后会有的版本（Hello 报告的、或最近排上的）；空 = 不确定，下次一律发全量
	latestVersion string
	// latest 是 latestVersion 对应的名单（Hello 报告、还没算过时为 nil）
	latest *NodeRoster
	// sent 是最近真正发出去（出队）或 Hello 时已一致的名单：名单成员判定与增量的起点按它
	sent *NodeRoster
	// confirmed 是节点确认在用的版本（Hello 报告或 UsersAck ok）；增量只在 confirmed == sent.Version 时发
	confirmed string
	// lastForced 是最近一次因 UsersAck 失败重发全量的时刻；forcePending 表示有一次重发在等间隔
	lastForced   time.Time
	forcePending bool
}

// configReleaseID 是一份全量配置的内容哈希：按给定顺序（节点 id 升序）拼「节点 id:摘要」。
func configReleaseID(sorted []NodeConfigDoc) string {
	h := sha256.New()
	for _, d := range sorted {
		h.Write([]byte(d.NodeID.String()))
		h.Write([]byte{':'})
		h.Write([]byte(d.SHA256))
		h.Write([]byte{'\n'})
	}
	return "r1-" + hex.EncodeToString(h.Sum(nil))
}

// applyConfig 收一次配置计算的结果：与节点（将）有的配置有任一不同就排一份全量，否则不发。
// 序号不比上次大的结果直接丢掉（见 session.go 文件头）。
func (s *Session) applyConfig(seq uint64, docs []NodeConfigDoc) {
	want := make(map[uuid.UUID]string, len(docs))
	for _, d := range docs {
		want[d.NodeID] = d.SHA256
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq <= s.cfgSeq || !s.acceptingLocked() {
		return
	}
	s.cfgSeq = seq
	if maps.Equal(want, s.cfgWant) {
		return
	}
	s.cfgWant = want
	sorted := slices.Clone(docs)
	slices.SortFunc(sorted, func(a, b NodeConfigDoc) int { return sessionCmpID(a.NodeID, b.NodeID) })
	s.putConfigLocked(Outbound{Kind: OutConfig, Config: ServerConfigOut{ReleaseID: configReleaseID(sorted), Nodes: sorted}},
		maps.Clone(want))
}

// applyRosters 收一次名单计算的结果（这台服务器每个该跑的节点一份）：逐节点比对，只发变了的。
// 不在结果里的节点（已停）撤掉没发出的名单，状态置为不确定（回来时发全量）；已发名单留着做成员判定。
func (s *Session) applyRosters(seq uint64, rosters []NodeRoster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if seq <= s.usersSeq || !s.acceptingLocked() {
		return
	}
	s.usersSeq = seq
	seen := make(map[uuid.UUID]bool, len(rosters))
	for i := range rosters {
		r := &rosters[i]
		seen[r.NodeID] = true
		st := s.nodes[r.NodeID]
		if st == nil {
			st = &rosterState{}
			s.nodes[r.NodeID] = st
		}
		s.offerRosterLocked(st, r)
	}
	for id, st := range s.nodes {
		if !seen[id] {
			st.latestVersion, st.latest, st.confirmed = "", nil, ""
			s.dropRosterLocked(id)
		}
	}
}

// offerRosterLocked 处理一个节点的新名单 r。
func (s *Session) offerRosterLocked(st *rosterState, r *NodeRoster) {
	if st.latestVersion == r.Version {
		if st.latest == nil {
			// Hello 报告的版本与算出的一致：节点已有这份名单
			st.latest = r
			if st.sent == nil && st.confirmed == r.Version {
				st.sent = r
			}
		}
		return
	}
	st.latestVersion, st.latest = r.Version, r
	if st.sent != nil && st.sent.Version == r.Version && st.confirmed == r.Version {
		// 回到了节点已确认的版本：撤掉没发出的，什么也不用发
		s.dropRosterLocked(r.NodeID)
		return
	}
	s.putRosterLocked(r.NodeID, s.rosterMessageLocked(st, r))
}

// rosterMessageLocked 选增量还是全量：双方支持增量、节点确认在用的正是上次发出的版本、增量比全量小，才发增量。
func (s *Session) rosterMessageLocked(st *rosterState, r *NodeRoster) Outbound {
	if s.delta && st.sent != nil && st.confirmed != "" && st.confirmed == st.sent.Version && st.sent.Version != r.Version {
		upserted, removed := diffRoster(st.sent.Users, r.Users)
		if len(upserted)+len(removed) < len(r.Users) {
			return Outbound{Kind: OutUsersDelta, Roster: r, DeltaFrom: st.sent.Version, Upserted: upserted, Removed: removed}
		}
	}
	return Outbound{Kind: OutUsersFull, Roster: r}
}

// diffRoster 比较两份按 UID 升序的名单：新增或属性有变的（按 to 的值），以及移除的 UID（升序）。
func diffRoster(from, to []RosterUser) (upserted []RosterUser, removed []int64) {
	i, j := 0, 0
	for i < len(from) || j < len(to) {
		switch {
		case j >= len(to) || (i < len(from) && from[i].UID < to[j].UID):
			removed = append(removed, from[i].UID)
			i++
		case i >= len(from) || to[j].UID < from[i].UID:
			upserted = append(upserted, to[j])
			j++
		default:
			if from[i] != to[j] {
				upserted = append(upserted, to[j])
			}
			i++
			j++
		}
	}
	return upserted, removed
}

// HandleUsersAck 处理名单回执：ok 记为节点确认在用的版本；失败（version_mismatch、apply_failed 等）发这个节点的全量。
// 同一节点因失败重发全量至少隔 ForcedFullGap，防节点反复装不上时空转。
func (s *Session) HandleUsersAck(a UsersAck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.nodes[a.NodeID]
	if st == nil {
		return
	}
	if a.OK {
		st.confirmed = a.Version
		return
	}
	st.confirmed = ""
	s.forceFullLocked(a.NodeID, st)
}

// forceFullLocked 排这个节点最新名单的全量；离上次强制重发不到 ForcedFullGap 就延后到间隔满了再排。
func (s *Session) forceFullLocked(nodeID uuid.UUID, st *rosterState) {
	if st.latest == nil || st.forcePending || !s.acceptingLocked() {
		return
	}
	now := s.m.now()
	if wait := s.m.opt.ForcedFullGap - now.Sub(st.lastForced); !st.lastForced.IsZero() && wait > 0 {
		st.forcePending = true
		time.AfterFunc(wait, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			st.forcePending = false
			if s.nodes[nodeID] == st && st.confirmed == "" {
				s.forceFullLocked(nodeID, st)
			}
		})
		return
	}
	st.lastForced = now
	s.putRosterLocked(nodeID, Outbound{Kind: OutUsersFull, Roster: st.latest})
}

// Has 实现 RosterMembership：最近真正发给这台服务器的名单里，节点 nodeID 有没有 uid。
func (s *Session) Has(nodeID uuid.UUID, uid int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.nodes[nodeID]
	if st == nil || st.sent == nil {
		return false
	}
	users := st.sent.Users
	i := sort.Search(len(users), func(k int) bool { return users[k].UID >= uid })
	return i < len(users) && users[i].UID == uid
}

var _ RosterMembership = (*Session)(nil)

// configAckExpected 报告这条回执的摘要是不是这个节点最近发出（或 Hello 时已一致）的那份。
func (s *Session) configAckExpected(nodeID uuid.UUID, sha string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	want, ok := s.cfgSent[nodeID]
	return ok && want == sha
}
