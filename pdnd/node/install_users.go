package node

import (
	"errors"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 启动顺序：先有名单、再开 accept。
//
// 装入站（冷启动、换配置都会重建入站）之后才装用户的话，监听已经在接客、内核用户表还是空的，
// 这段时间进来的连接全被拒成「用户未授权」（10 万连接重启风暴实测约 77 次）。所以装入站之前
// 先把名单准备好，经 ApplyInboundWithUsers 一起交给内核：内核先装名单、再 Start。
//
// 名单从哪来，按次序：
//  1. 内核里现有的名单 n.known（换配置重建入站时，就是旧入站在用的那份；版本不变）；
//  2. 入站起来之前收到的面板全量 n.staged；
//  3. 冷启动（n.offlineStart）且 1、2 都没有时，落盘缓存的名单；
//  4. 都没有：带空名单装（版本为空），等面板发全量。

// usersApplier 是内核「带名单装入站」的可选契约（NativeCore 实现）。
type usersApplier interface {
	ApplyInboundWithUsers(*core.InboundConfig, *core.Routing, []core.User) error
}

// preparedUsers 是装入站时一起交给内核的名单与它的来源。
type preparedUsers struct {
	users   []core.User
	version string
	// source 是名单来源：known / staged / cache / none。
	source string
	// failed：入站装上了但名单没装上（UsersPreloadError），按「内核用户表已空」处理。
	failed bool
}

// prepareInstallUsers 在装入站之前准备名单；内核不支持带名单装入站时返回 nil。
func (n *Node) prepareInstallUsers() *preparedUsers {
	if _, ok := n.kernel.(usersApplier); !ok {
		return nil
	}
	if len(n.known) > 0 {
		users := make([]core.User, 0, len(n.known))
		for _, u := range n.known {
			users = append(users, u)
		}
		return &preparedUsers{users: users, version: n.userVersion, source: "known"}
	}
	if n.staged != nil {
		return &preparedUsers{users: normalizeUsers(n.staged.users), version: n.staged.version, source: "staged"}
	}
	if n.offlineStart && n.cache != nil {
		version, users, err := n.cache.loadUsers()
		if err == nil {
			return &preparedUsers{users: normalizeUsers(users), version: version, source: "cache"}
		}
		n.log.Warn("落盘名单不可用，先带空名单起服务，等面板发全量", "err", err)
	}
	return &preparedUsers{source: "none"}
}

// adoptInstalledUsers 在入站装好之后把本地用户镜像对齐到「内核里已是这份名单」。
func (n *Node) adoptInstalledUsers(p *preparedUsers) {
	if p == nil {
		// 兼容内核不能带名单装，入站重建清空了内核用户表：把手上的名单补回去
		// （没有就装入站之前收到的全量）。补回失败就整个作废，等面板发全量。
		if len(n.known) == 0 {
			n.applyStagedAfterInstall()
			return
		}
		users := make([]core.User, 0, len(n.known))
		for _, u := range n.known {
			users = append(users, u)
		}
		if err := n.kernel.AddUsers(n.tag, users); err != nil {
			n.log.Warn("入站重建后补回名单失败，等面板发全量", "err", err)
			n.resetUserMirror()
		}
		return
	}
	if p.failed {
		n.resetUserMirror()
		if n.pendingUsersAck == nil && p.version != "" {
			// 名单版本没有记成「内核里已是」（如入站之前收到的全量），resync 回执不会发；
			// 照样告诉面板这一版没装上。
			n.pendingUsersAck = &nodev1.UsersAck{NodeId: n.id, Version: p.version, Ok: false, ReasonCode: panel.UsersReasonApplyFailed}
		}
		n.staged = nil
		return
	}
	n.known = mirrorOf(p.users)
	n.userVersion = p.version
	switch p.source {
	case "staged":
		n.staged = nil
		n.usersDirty = true
		n.pendingUsersAck = &nodev1.UsersAck{NodeId: n.id, Version: p.version, Ok: true}
	case "cache":
		n.usersDirty = false
	}
}

// applyStagedAfterInstall 给兼容内核用：入站起来之后再装入站之前收到的全量。
func (n *Node) applyStagedAfterInstall() {
	st := n.staged
	if st == nil {
		return
	}
	n.staged = nil
	if err := n.applyUsers(st.users); err != nil {
		n.resetUserMirror()
		n.pendingUsersAck = &nodev1.UsersAck{NodeId: n.id, Version: st.version, Ok: false, ReasonCode: panel.UsersReasonApplyFailed}
		n.log.Warn("入站起来之后装名单失败，等面板重发全量", "err", err)
		return
	}
	n.userVersion = st.version
	n.pendingUsersAck = &nodev1.UsersAck{NodeId: n.id, Version: st.version, Ok: true}
}

// isUsersPreloadError 认出内核的「入站已就绪、名单没装上」（kernel.UsersPreloadError）。
func isUsersPreloadError(err error) bool {
	var target interface{ UsersPreloadFailed() bool }
	return errors.As(err, &target) && target.UsersPreloadFailed()
}
