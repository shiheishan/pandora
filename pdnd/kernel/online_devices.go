package kernel

import (
	"slices"
	"sync"

	"github.com/aegispanel/nodeagent/core"
)

// onlineDevices 记各用户当前在线的设备，以及每台设备上的在途连接数，
// 供在线上报（OnlineIPs）与设备数限制共用。各协议适配器都内嵌一份，零值即可用。
//
// 设备按来源地址计（core.DeviceKey）：IPv4（含 IPv4 映射地址）按单个地址，IPv6 按 /64
// 网段（用户 10-09 定：运营商给一户分一个 /64，隐私扩展地址在其中随时轮换，按单个
// 地址计会把一台设备算成好几台）。同一设备上的多条连接只占一个名额，连接数减到 0
// 才算离线。早先各适配器各写一份不带计数的 map[用户]map[IP]struct{}：同一 IP 开两条
// 连接、断掉任意一条就把这个 IP 整个删了，剩下那条还在转发却不再上报在线，设备名额
// 也被腾了出来，关一条连接就能多接一台设备（计费与限设备都被绕过）。
//
// 上报给面板的是设备键本身（IPv4 地址或 "前缀/64"），不是原始地址：面板对上报串
// 取哈希、按订阅 count(DISTINCT) 计设备数（nodefabric 的 aliveRows 与
// device_limit_admin），报原始 IPv6 地址会把同一 /64 的轮换地址算成多台、按面板侧
// 宽松/严格模式误停用户；报设备键则节点与面板同一口径，跨节点也按同一键去重。
//
// 用法固定成「enter 成功后 defer leave」，enter 与 leave 一一对应；
// 连接异常退出也走 defer，不会漏减。
type onlineDevices struct {
	mu sync.Mutex
	// 用户 ID → 设备键（core.DeviceKey）→ 该设备上的在途连接数（恒 ≥ 1）
	byUser map[int64]map[string]int
}

// enter 登记一条来自 ip 的连接。用户设了设备上限（DeviceLimit > 0）且这是一台
// 新设备、不同设备数已到上限时拒绝，什么也不登记；已在线的设备再来连接一律放行。
func (d *onlineDevices) enter(user core.User, ip string) bool {
	ip = core.DeviceKey(ip)
	d.mu.Lock()
	defer d.mu.Unlock()
	ips := d.byUser[user.ID]
	if n, ok := ips[ip]; ok {
		ips[ip] = n + 1
		return true
	}
	if user.DeviceLimit > 0 && len(ips) >= user.DeviceLimit {
		return false
	}
	if ips == nil {
		if d.byUser == nil {
			d.byUser = make(map[int64]map[string]int)
		}
		ips = make(map[string]int, 1)
		d.byUser[user.ID] = ips
	}
	ips[ip] = 1
	return true
}

// leave 撤销一次成功的 enter（传同一个 ip）。计数减到 0 才删设备，用户没有在线设备
// 时连同这一层一起回收。对没登记过的 (用户, 设备) 什么也不做，计数不会变负。
func (d *onlineDevices) leave(user core.User, ip string) {
	ip = core.DeviceKey(ip)
	d.mu.Lock()
	defer d.mu.Unlock()
	ips := d.byUser[user.ID]
	n, ok := ips[ip]
	if !ok {
		return
	}
	if n > 1 {
		ips[ip] = n - 1
		return
	}
	delete(ips, ip)
	if len(ips) == 0 {
		delete(d.byUser, user.ID)
	}
}

// snapshot 返回当前在线的设备键（每个用户的已排序、不重复），供上报使用。
func (d *onlineDevices) snapshot() map[int64][]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[int64][]string, len(d.byUser))
	for id, ips := range d.byUser {
		list := make([]string, 0, len(ips))
		for ip := range ips {
			list = append(list, ip)
		}
		slices.Sort(list)
		out[id] = list
	}
	return out
}
