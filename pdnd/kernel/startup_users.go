package kernel

import "github.com/aegispanel/nodeagent/core"

// 启动顺序：用户名单先于 accept。
//
// 原先节点先 ApplyInbound（监听立刻开始接客），再去面板拉用户、AddUsers。中间
// 这段（一次面板往返，冷启动重连风暴里实测几毫秒到几百毫秒）进来的连接全被当成
// 「用户未授权」拒掉——10 万连接重启时出现约 77 次。现在 ApplyInboundWithUsers
// 在 Start 之前就把名单装进适配器，监听一开名单已在。
//
// 只有「构造时就建好用户表、Start 时把已有用户同步进服务」的适配器能这样做。
// mieru 不行：它的首批用户会直接把监听拉起来（那时出站还没接上），对它退回
// Start 之后立刻补装。

// preStartUsers 标记 Start 之前 AddUsers 是安全的适配器。
type preStartUsers interface {
	usersBeforeStart()
}

func (a *vlessAdapter) usersBeforeStart()       {}
func (a *vmessAdapter) usersBeforeStart()       {}
func (a *trojanAdapter) usersBeforeStart()      {}
func (a *shadowsocksAdapter) usersBeforeStart() {}
func (a *ss2022Adapter) usersBeforeStart()      {}
func (a *proxyAdapter) usersBeforeStart()       {}
func (a *naiveAdapter) usersBeforeStart()       {}
func (a *anyTLSAdapter) usersBeforeStart()      {}
func (a *hysteria2Adapter) usersBeforeStart()   {}
func (a *tuicAdapter) usersBeforeStart()        {}
func (a *juicityAdapter) usersBeforeStart()     {}
func (a *shadowTLSAdapter) usersBeforeStart()   {}

// preloadUsers 在 Start 之前装名单（适配器支持时）。
func preloadUsers(a Adapter, users []core.User) error {
	if len(users) == 0 {
		return nil
	}
	if _, ok := a.(preStartUsers); !ok {
		return nil
	}
	return a.AddUsers(users)
}

// postloadUsers 给不支持预装的适配器在 Start 之后立刻补装。
func postloadUsers(a Adapter, users []core.User) error {
	if len(users) == 0 {
		return nil
	}
	if _, ok := a.(preStartUsers); ok {
		return nil
	}
	return a.AddUsers(users)
}

// UsersPreloadError：入站已经装好并在服务，但随入站一起交来的名单没装上。
// 调用方应把它当「入站成功、用户待同步」处理，而不是配置失败。
type UsersPreloadError struct{ Err error }

func (e *UsersPreloadError) Error() string {
	return "入站已就绪，但名单没装上: " + e.Err.Error()
}
func (e *UsersPreloadError) Unwrap() error { return e.Err }

// UsersPreloadFailed 让只认 core 抽象的调用方（node）不 import kernel 也能认出它。
func (e *UsersPreloadError) UsersPreloadFailed() bool { return true }
