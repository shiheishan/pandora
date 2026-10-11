package node

import (
	"context"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// applyUsers 把一份完整用户列表落到内核上。
//
// 轮询和事件流两条路都走这里——所有对 n.known 的改动集中在一个地方，
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
// 节点端对内核用户表的认知有三份：n.known（算 diff 用）、n.userVersion
// （判断增量能不能打）、客户端里的用户 ETag（换 304 用）。三者说的是
// 同一件事——「内核里已经是这一版了」——所以只能在这一处一起作废：
// 漏掉 ETag，下一轮拉用户换回 304，内核一直是空表；漏掉 userVersion，
// 基于旧版的增量会被打在空表上，只剩增量里新加的那几个人。
func (n *Node) resetUserMirror() {
	n.known = make(map[int64]core.User)
	n.userVersion = ""
	n.client.ForgetUsersVersion()
}

// markInboundLost 在回滚也失败时调用：入站已不可用，内核用户表处于未知
// 状态。下一次 applyConfig 成功前不同步用户（syncOnce 看 started），
// 成功之后从无条件全量开始。
func (n *Node) markInboundLost() {
	n.started = false
	n.resetUserMirror()
}

// applyUserDelta 在现有列表上打补丁。
//
// 增量按用户 ID 说话：Removed 是「这个 ID 不再放行」，Added 里的一条是「这个 ID
// 现在的完整记录」。同一 ID 两边都出现时以 Added 为准（面板对换了凭据的用户就是
// 先删后加）。只看 Added 也要挡住换凭据：Added 的 UUID 与手上的不同，就删掉手上那份
// 旧凭据并断开它的连接，不让新旧凭据并存——用户重置订阅后，旧链接必须立刻失效。
// Added 里凭据为空的记录按「这个 ID 已没有可用凭据」处理（删旧，不加）。
func (n *Node) applyUserDelta(ev panel.StreamEvent) error {
	next := deltaTargets(ev)
	var removed []string
	var added, updated []core.User
	for _, u := range next {
		before, exists := n.known[u.ID]
		credentialChanged := !exists || before.UUID != u.UUID
		if exists && credentialChanged {
			removed = append(removed, before.UUID)
		}
		switch {
		case u.UUID == "":
		case credentialChanged:
			added = append(added, u)
		case before != u:
			updated = append(updated, u)
		}
	}
	if err := n.applyKernelUsers(removed, added, updated); err != nil {
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
		"总数", len(n.known), "新增", len(added), "更新", len(updated), "移除", len(removed))
	return nil
}

// deltaTargets 把增量化成「每个涉及的用户 ID 一条终态记录」：Removed 的 ID 终态是
// 空凭据，Added 覆盖它；同一 ID 多次出现以最后一条为准（面板不会这样发，万一发了，
// 装进内核的与镜像里记的也得是同一份，否则多出来的凭据以后永远删不掉）。
// 绝大多数增量只动一个用户，那时不建表。
func deltaTargets(ev panel.StreamEvent) []core.User {
	switch {
	case len(ev.Removed) == 0 && len(ev.Added) <= 1:
		return ev.Added
	case len(ev.Removed) == 1 && len(ev.Added) == 0:
		return []core.User{{ID: ev.Removed[0]}}
	}
	out := make([]core.User, 0, len(ev.Removed)+len(ev.Added))
	at := make(map[int64]int, cap(out))
	put := func(u core.User) {
		if i, seen := at[u.ID]; seen {
			out[i] = u
			return
		}
		at[u.ID] = len(out)
		out = append(out, u)
	}
	for _, id := range ev.Removed {
		put(core.User{ID: id})
	}
	for _, u := range ev.Added {
		put(u)
	}
	return out
}

func (n *Node) syncUsers(ctx context.Context) error {
	users, changed, err := n.client.Users(ctx)
	if err != nil {
		return err
	}
	if !changed {
		// 面板回了 304，列表和上一轮一样。直接返回——不能往下走：
		// changed 为 false 时 users 是 nil，下面那段会把它当成「一个用户
		// 都没有」，然后把所有人从内核里删掉。
		return nil
	}

	if err := n.applyUsers(users); err != nil {
		// 客户端在解析成功时已记下这一版的 ETag，内核却没装齐：作废它与增量
		// 基准，下一轮无条件全量对齐。不作废的话下一轮拿它换回 304，这份名单
		// 就再也装不上了；增量也不能再按旧基准往上打。
		n.userVersion = ""
		n.client.ForgetUsersVersion()
		return err
	}
	// 记下这一版作为增量基准。REST 的 ETag 与事件流的版本出自面板同一个计算
	// 函数，装上了全量就等于手上是这一版：基于它的增量直接打，不必再拉一次
	// 全量（原先这里不记，轮询之后的每条增量都对不上基准、退化成拉全量）。
	n.userVersion = panel.UsersVersionKey(n.client.UsersVersion())
	return nil
}
