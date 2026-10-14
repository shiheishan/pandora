package node

import (
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// applyUsers 把一份完整用户列表落到内核上。
//
// 全量名单都走这里——所有对 n.known 的改动集中在一个地方，
// 才好保证它和内核里的实际状态一致。
//
// 按用户 ID 对比：同一 ID 换了凭据（重置订阅换 proxy_uuid）是「删旧凭据、加新凭据」，
// 凭据没变、只改了限速或设备数是原地更新（不断线）。
func (n *Node) applyUsers(users []core.User) error {
	// 与 normalizeUsers 同一口径（空凭据不要、重复 ID 留最后一条），直接建表，
	// 不先复制一遍名单：全量对齐的开销就是这一张表。
	want := make(map[int64]core.User, len(users))
	for _, u := range users {
		if u.UUID != "" {
			want[u.ID] = u
		}
	}
	var removed []string
	var added, updated []core.User
	for id, before := range n.known {
		if u, ok := want[id]; !ok || u.UUID != before.UUID {
			removed = append(removed, before.UUID)
		}
	}
	for id, u := range want {
		before, exists := n.known[id]
		switch {
		case !exists || before.UUID != u.UUID:
			added = append(added, u)
		case before != u:
			updated = append(updated, u)
		}
	}
	if err := n.applyKernelUsers(removed, added, updated); err != nil {
		return err
	}
	n.known = want
	n.usersDirty = true

	if len(added) > 0 || len(updated) > 0 || len(removed) > 0 {
		n.log.Info("用户已同步", "总数", len(want), "新增", len(added), "更新", len(updated), "移除", len(removed))
	}
	return nil
}

// applyKernelUsers 把一批名单变更落到内核：先删、再加、再改。
//
// 先删是因为内核按用户 ID 断线：DelUsers 删掉一条凭据时，会断开这个用户 ID 名下的
// 已有连接。同一 ID 换凭据时先断旧凭据的连接、再装新凭据；反过来的话，新凭据装上
// 之后、旧凭据删掉之前用新凭据连上的会话也会被一并踢掉。
func (n *Node) applyKernelUsers(removed []string, added, updated []core.User) error {
	if len(removed) > 0 {
		if err := n.kernel.DelUsers(n.tag, removed); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if err := n.kernel.AddUsers(n.tag, added); err != nil {
			return err
		}
	}
	if len(updated) > 0 {
		if err := n.kernel.UpsertUsers(n.tag, updated); err != nil {
			return err
		}
	}
	return nil
}

// normalizeUsers 去掉没有凭据的记录；同一 ID 出现多次时只留最后一条。
//
// 面板名单里 ID 与 UUID 一一对应，重复 ID 不该出现；万一出现，装进内核的与
// 镜像里记的必须是同一份，否则多出来的那条凭据镜像里没有，以后永远删不掉。
func normalizeUsers(users []core.User) []core.User {
	out := users[:0:0]
	at := make(map[int64]int, len(users))
	for _, u := range users {
		if u.UUID == "" {
			continue
		}
		if i, dup := at[u.ID]; dup {
			out[i] = u
			continue
		}
		at[u.ID] = len(out)
		out = append(out, u)
	}
	return out
}

// mirrorOf 把一份已经 normalizeUsers 过的名单做成按 ID 记的镜像。
func mirrorOf(users []core.User) map[int64]core.User {
	m := make(map[int64]core.User, len(users))
	for _, u := range users {
		m[u.ID] = u
	}
	return m
}

// resetUserMirror 宣告「内核里的用户表已被清空」。
//
// 节点端对内核用户表的认知有两份：n.known（算 diff 用）、n.userVersion（判断增量能不能打、
// Hello 里报给面板）。两者说的是同一件事——「内核里已经是这一版了」——所以只能在这一处
// 一起作废：漏掉 userVersion，基于旧版的增量会被打在空表上，只剩增量里新加的那几个人。
// 作废前有版本的，回一条 ok=false/resync 的名单回执，面板据此发全量。
func (n *Node) resetUserMirror() {
	prev := n.userVersion
	n.known = make(map[int64]core.User)
	n.userVersion = ""
	if prev != "" {
		n.pendingUsersAck = &nodev1.UsersAck{NodeId: n.id, Version: prev, Ok: false, ReasonCode: panel.UsersReasonResync}
	}
}

// markInboundLost 在回滚也失败时调用：入站已不可用，内核用户表处于未知
// 状态。下一次 applyConfig 成功前不装名单（入站没起来时收到的全量先存着），
// 成功之后从全量开始。
func (n *Node) markInboundLost() {
	n.started = false
	n.resetUserMirror()
}

// applyUserDelta 在现有列表上打补丁。
//
// 增量按用户 ID 说话：removed 是「这个 ID 不再放行」，upserted 里的一条是「这个 ID
// 现在的完整记录」。同一 ID 两边都出现时以 upserted 为准（面板对换了凭据的用户就是
// 先删后加）。只看 upserted 也要挡住换凭据：UUID 与手上的不同，就删掉手上那份
// 旧凭据并断开它的连接，不让新旧凭据并存——用户重置订阅后，旧链接必须立刻失效。
// upserted 里凭据为空的记录按「这个 ID 已没有可用凭据」处理（删旧，不加）。
func (n *Node) applyUserDelta(upserted []core.User, removed []int64) error {
	next := deltaTargets(upserted, removed)
	var dropped []string
	var added, updated []core.User
	for _, u := range next {
		before, exists := n.known[u.ID]
		credentialChanged := !exists || before.UUID != u.UUID
		if exists && credentialChanged {
			dropped = append(dropped, before.UUID)
		}
		switch {
		case u.UUID == "":
		case credentialChanged:
			added = append(added, u)
		case before != u:
			updated = append(updated, u)
		}
	}
	if err := n.applyKernelUsers(dropped, added, updated); err != nil {
		return err
	}
	for _, u := range next {
		if u.UUID == "" {
			delete(n.known, u.ID)
		} else {
			n.known[u.ID] = u
		}
	}
	n.usersDirty = true
	n.log.Info("用户增量已应用",
		"总数", len(n.known), "新增", len(added), "更新", len(updated), "移除", len(dropped))
	return nil
}

// deltaTargets 把增量化成「每个涉及的用户 ID 一条终态记录」：Removed 的 ID 终态是
// 空凭据，upserted 覆盖它；同一 ID 多次出现以最后一条为准（面板不会这样发，万一发了，
// 装进内核的与镜像里记的也得是同一份，否则多出来的凭据以后永远删不掉）。
// 绝大多数增量只动一个用户，那时不建表。
func deltaTargets(upserted []core.User, removed []int64) []core.User {
	switch {
	case len(removed) == 0 && len(upserted) <= 1:
		return upserted
	case len(removed) == 1 && len(upserted) == 0:
		return []core.User{{ID: removed[0]}}
	}
	out := make([]core.User, 0, len(removed)+len(upserted))
	at := make(map[int64]int, cap(out))
	put := func(u core.User) {
		if i, seen := at[u.ID]; seen {
			out[i] = u
			return
		}
		at[u.ID] = len(out)
		out = append(out, u)
	}
	for _, id := range removed {
		put(core.User{ID: id})
	}
	for _, u := range upserted {
		put(u)
	}
	return out
}

// usersFromProto 把面板名单换成内核口径。
func usersFromProto(in []*nodev1.User) []core.User {
	out := make([]core.User, 0, len(in))
	for _, u := range in {
		out = append(out, core.User{
			ID: u.GetUid(), UUID: u.GetUuid(),
			SpeedLimit: int(u.GetSpeedLimitKbps()), DeviceLimit: int(u.GetDeviceLimit()),
		})
	}
	return out
}

// onUsersFull 处理某节点的全量名单。
//
// 入站还没起来（面板先发名单后发配置，或配置装不上）时先存着，装入站时带上（先有名单、
// 再开 accept），装上之后再回执。节点还不存在时先建一个占位节点存名单：它不进 s.order，
// 下一份 ServerConfig 里没有它就会被删。
func (s *Server) onUsersFull(full *panel.UsersFull) {
	if full == nil || full.NodeID == "" {
		return
	}
	n := s.nodes[full.NodeID]
	if n == nil {
		n = s.newNode(full.NodeID, "")
		s.nodes[full.NodeID] = n
	}
	users := normalizeUsers(usersFromProto(full.Users))
	if !n.started {
		n.staged = &stagedUsers{version: full.Version, users: users}
		n.log.Info("入站还没起来，名单先存着，装入站时一起装", "版本", full.Version, "用户数", len(users))
		return
	}
	if err := n.applyUsers(users); err != nil {
		// 内核没装齐：作废增量基准，面板收到 apply_failed 会重发全量。
		n.userVersion = ""
		s.sendUsersAck(n, full.Version, false, panel.UsersReasonApplyFailed)
		n.log.Error("名单装进内核失败，等面板重发全量", "版本", full.Version, "err", err)
	} else {
		n.userVersion = full.Version
		n.staged = nil
		s.sendUsersAck(n, full.Version, true, "")
	}
	s.flushUsersAck(n)
	s.publishApplied()
}

// onUsersDelta 处理名单增量：只在本地确实是 from_version 时打，否则回 version_mismatch，
// 面板据此发全量。
func (s *Server) onUsersDelta(d *nodev1.UsersDelta) {
	if d == nil {
		return
	}
	n := s.nodes[d.GetNodeId()]
	if n == nil || !n.started || n.userVersion == "" || d.GetFromVersion() != n.userVersion {
		local := ""
		if n != nil {
			local = n.userVersion
		}
		s.log.Info("增量基准版本对不上，等面板发全量", "node", d.GetNodeId(),
			"基准", d.GetFromVersion(), "本地", local)
		s.sendUsersAckByID(d.GetNodeId(), d.GetToVersion(), false, panel.UsersReasonVersionMismatch)
		return
	}
	if err := n.applyUserDelta(usersFromProto(d.GetUpserted()), d.GetRemovedUids()); err != nil {
		n.userVersion = ""
		s.sendUsersAck(n, d.GetToVersion(), false, panel.UsersReasonApplyFailed)
		n.log.Error("名单增量装进内核失败，等面板重发全量", "err", err)
	} else {
		n.userVersion = d.GetToVersion()
		s.sendUsersAck(n, d.GetToVersion(), true, "")
	}
	s.flushUsersAck(n)
	s.publishApplied()
}

// sendUsersAck 回一条名单回执；没连着就不发（重连的 Hello 会报真实版本）。
func (s *Server) sendUsersAck(n *Node, version string, ok bool, reason string) bool {
	return s.sendUsersAckByID(n.id, version, ok, reason)
}

func (s *Server) sendUsersAckByID(nodeID, version string, ok bool, reason string) bool {
	if !s.connected || s.link == nil {
		return false
	}
	return s.link.SendUsersAck(&nodev1.UsersAck{NodeId: nodeID, Version: version, Ok: ok, ReasonCode: reason})
}
