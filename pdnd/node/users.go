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
	if len(ev.Removed) > 0 {
		// 增量里的 Removed 是用户 ID，内核按 UUID 删，要先换算。
		byID := make(map[int64]string, len(n.known))
		for uuid, u := range n.known {
			byID[u.ID] = uuid
		}
		uuids := make([]string, 0, len(ev.Removed))
		for _, id := range ev.Removed {
			if uuid, ok := byID[id]; ok {
				uuids = append(uuids, uuid)
			}
		}
		if len(uuids) > 0 {
			if err := n.kernel.DelUsers(n.tag, uuids); err != nil {
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
	for _, id := range ev.Removed {
		for uuid, user := range n.known {
			if user.ID == id {
				delete(n.known, uuid)
			}
		}
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

	return n.applyUsers(users)
}
