package kernel

import (
	"net/netip"
	"slices"
	"sync"

	"github.com/aegispanel/nodeagent/core"
)

// onlineDevices 记各用户当前在线的来源 IP，以及每个 IP 上的在途连接数，
// 供在线上报（OnlineIPs）与设备数限制共用。各协议适配器都内嵌一份，零值即可用。
//
// 设备按来源 IP 计：同一 IP 上的多条连接只占一个名额，连接数减到 0 才算离线。
// 早先各适配器各写一份不带计数的 map[用户]map[IP]struct{}：同一 IP 开两条连接、
// 断掉任意一条就把这个 IP 整个删了，剩下那条还在转发却不再上报在线，
// 设备名额也被腾了出来，关一条连接就能多接一台设备（计费与限设备都被绕过）。
//
// 用法固定成「enter 成功后 defer leave」，enter 与 leave 一一对应；
// 连接异常退出也走 defer，不会漏减。
type onlineDevices struct {
	mu sync.Mutex
	// 用户 ID → 规范化后的来源 IP → 该 IP 上的在途连接数（恒 ≥ 1）
	byUser map[int64]map[string]int
}

// enter 登记一条来自 ip 的连接。用户设了设备上限（DeviceLimit > 0）且这是一个
// 新 IP、不同 IP 数已到上限时拒绝，什么也不登记；已在线的 IP 再来连接一律放行。
func (d *onlineDevices) enter(user core.User, ip string) bool {
	ip = normalizeDeviceIP(ip)
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

// leave 撤销一次成功的 enter。计数减到 0 才删 IP，用户没有在线 IP 时连同这一层一起回收。
// 对没登记过的 (用户, IP) 什么也不做，计数不会变负。
func (d *onlineDevices) leave(user core.User, ip string) {
	ip = normalizeDeviceIP(ip)
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

// snapshot 返回当前在线的 IP（每个用户的 IP 已排序、不重复），供上报使用。
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

// normalizeDeviceIP 把同一来源的不同写法归一：双栈监听上的 IPv4 映射地址
// （::ffff:a.b.c.d）按 IPv4 计，去掉 IPv6 zone。解析不了的（如 "unknown"）原样作键。
func normalizeDeviceIP(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	return addr.Unmap().WithZone("").String()
}
