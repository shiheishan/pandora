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
func (n *Node) applyUsers(users []core.User) error {
	want := make(map[string]core.User, len(users))
	var added, updated []core.User
	for _, u := range users {
		if u.UUID == "" {
			continue
		}
		want[u.UUID] = u
		before, exists := n.known[u.UUID]
		switch {
		case !exists:
			added = append(added, u)
		case before != u:
			updated = append(updated, u)
		}
	}
	var removed []string
	for uuid := range n.known {
		if _, ok := want[uuid]; !ok {
			removed = append(removed, uuid)
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
	if len(removed) > 0 {
		if err := n.kernel.DelUsers(n.tag, removed); err != nil {
			return err
		}
	}
	n.known = want
	n.usersDirty = true

	if len(added) > 0 || len(updated) > 0 || len(removed) > 0 {
		n.log.Info("用户已同步", "总数", len(want), "新增", len(added), "更新", len(updated), "移除", len(removed))
	}
	return nil
}

// resetUserMirror 宣告「内核里的用户表已被清空」。
//
// 节点端对内核用户表的认知有三份：n.known（算 diff 用）、n.userVersion
// （判断增量能不能打）、客户端里的用户 ETag（换 304 用）。三者说的是
// 同一件事——「内核里已经是这一版了」——所以只能在这一处一起作废：
// 漏掉 ETag，下一轮拉用户换回 304，内核一直是空表；漏掉 userVersion，
// 基于旧版的增量会被打在空表上，只剩增量里新加的那几个人。
func (n *Node) resetUserMirror() {
	n.known = make(map[string]core.User)
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
func (n *Node) applyUserDelta(ev panel.StreamEvent) error {
	var added, updated []core.User
	for _, u := range ev.Added {
		if u.UUID == "" {
			continue
		}
		if before, exists := n.known[u.UUID]; exists && before != u {
			updated = append(updated, u)
		} else if !exists {
			added = append(added, u)
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
	// 增量里的 Removed 是用户 ID，内核与本地镜像都按 UUID 删，先换算一次。
	// 原先镜像那边是「每个删除 ID 扫一遍全表」，±500 人的增量比整体替换还慢。
	// 同一 ID 可能挂着不止一个 UUID（口令重置的中间态），全部删。
	var removedUUIDs []string
	if len(ev.Removed) > 0 {
		byID := make(map[int64][]string, len(n.known))
		for uuid, u := range n.known {
			byID[u.ID] = append(byID[u.ID], uuid)
		}
		for _, id := range ev.Removed {
			removedUUIDs = append(removedUUIDs, byID[id]...)
		}
		if len(removedUUIDs) > 0 {
			if err := n.kernel.DelUsers(n.tag, removedUUIDs); err != nil {
				return err
			}
		}
	}
	for _, u := range added {
		n.known[u.UUID] = u
	}
	for _, u := range updated {
		n.known[u.UUID] = u
	}
	for _, uuid := range removedUUIDs {
		delete(n.known, uuid)
	}
	n.usersDirty = true
	n.log.Info("用户增量已应用",
		"总数", len(n.known), "新增", len(added), "更新", len(updated), "移除", len(ev.Removed))
	return nil
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
