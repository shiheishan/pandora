package node

import (
	"context"
	"errors"

	"github.com/aegispanel/nodeagent/core"
)

// 启动顺序：先有名单、再开 accept。
//
// 原先装入站（冷启动、换配置都会重建入站）之后才去面板拉用户：监听已经在接客、
// 内核用户表还是空的，这段时间进来的连接全被拒成「用户未授权」（10 万连接重启
// 风暴实测约 77 次）。现在装入站之前先把名单准备好，经 ApplyInboundWithUsers
// 一起交给内核：内核先装名单、再 Start。
//
// 名单从哪来，按可信度：
//  1. 面板全量（作废 ETag 后重拉）——客户端随之记下这一版的 ETag，紧接着的那轮
//     同步换回 304，不重复拉；
//  2. 拉不到时用手上的名单（换配置时就是旧入站在用的那份）；
//  3. 冷启动且面板不可达（startup 的落盘缓存路径）直接用落盘的名单，不再
//     多等一次面板超时。
// 都没有就退回原流程（装完入站再同步）。2、3 只是「先别带空表跑」，ETag 会被
// 作废或换成缓存的那版，下一轮照常与面板对齐。

// usersApplier 是内核「带名单装入站」的可选契约（NativeCore 实现）。
type usersApplier interface {
	ApplyInboundWithUsers(*core.InboundConfig, *core.Routing, []core.User) error
}

// preparedUsers 是装入站时一起交给内核的名单与它的来源。
type preparedUsers struct {
	users []core.User
	// fresh：刚从面板拉的全量，客户端里的用户 ETag 就是这一版。
	fresh bool
	// fromCache：来自落盘缓存，version / etag 是缓存里记的那一版。
	fromCache     bool
	version, etag string
	// failed：入站装上了但名单没装上（UsersPreloadError），按「没带名单」处理。
	failed bool
}

// prepareInstallUsers 在装入站之前准备名单；内核不支持带名单装入站时返回 nil。
func (n *Node) prepareInstallUsers(cfg map[string]any) *preparedUsers {
	if _, ok := n.kernel.(usersApplier); !ok {
		return nil
	}
	// 用户名单按节点协议查询；面板换了协议时先把客户端切过去（installConfig 里
	// 还会再调一次，幂等）。
	n.protocolFrom(cfg)
	if n.offlineStart {
		if p := n.cachedInstallUsers(); p != nil {
			return p
		}
		return n.knownInstallUsers()
	}
	ctx := n.lifeCtx
	if ctx == nil {
		ctx = context.Background()
	}
	n.client.ForgetUsersVersion()
	users, changed, err := n.client.Users(ctx)
	if err == nil && changed {
		return &preparedUsers{users: withUUID(users), fresh: true}
	}
	if err != nil {
		n.log.Warn("装入站前拉用户名单失败，先用手上的名单", "err", err)
	}
	if p := n.knownInstallUsers(); p != nil {
		return p
	}
	return n.cachedInstallUsers()
}

func (n *Node) knownInstallUsers() *preparedUsers {
	if len(n.known) == 0 {
		return nil
	}
	users := make([]core.User, 0, len(n.known))
	for _, u := range n.known {
		users = append(users, u)
	}
	return &preparedUsers{users: users}
}

func (n *Node) cachedInstallUsers() *preparedUsers {
	if n.cache == nil {
		return nil
	}
	var file cachedUsersFile
	if err := n.cache.read(n.cache.usersPath(), &file); err != nil {
		return nil
	}
	if file.Format != cacheFormat || file.NodeID != n.cache.nodeID || len(file.Users) == 0 {
		return nil
	}
	users := make([]core.User, 0, len(file.Users))
	for _, u := range file.Users {
		users = append(users, core.User{ID: u.ID, UUID: u.UUID, SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit})
	}
	return &preparedUsers{users: withUUID(users), fromCache: true, version: file.Version, etag: file.ETag}
}

// adoptInstalledUsers 在入站装好之后把本地用户镜像对齐到「内核里已是这份名单」。
// p 为 nil（没带名单装）时与原流程一样整个作废，等下一轮全量。
func (n *Node) adoptInstalledUsers(p *preparedUsers) {
	if p == nil || p.failed {
		n.resetUserMirror()
		return
	}
	known := make(map[string]core.User, len(p.users))
	for _, u := range p.users {
		known[u.UUID] = u
	}
	n.known = known
	switch {
	case p.fresh:
		n.userVersion = ""
		n.usersDirty = true
	case p.fromCache:
		n.userVersion = p.version
		n.client.SetUsersVersion(p.etag)
	default:
		n.userVersion = ""
		n.client.ForgetUsersVersion()
	}
}

// isUsersPreloadError 认出内核的「入站已就绪、名单没装上」（kernel.UsersPreloadError）。
func isUsersPreloadError(err error) bool {
	var target interface{ UsersPreloadFailed() bool }
	return errors.As(err, &target) && target.UsersPreloadFailed()
}

func withUUID(users []core.User) []core.User {
	out := users[:0:0]
	for _, u := range users {
		if u.UUID != "" {
			out = append(out, u)
		}
	}
	return out
}
