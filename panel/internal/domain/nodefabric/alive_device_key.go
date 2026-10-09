package nodefabric

import "net/netip"

// aliveDeviceKey 把节点上报的一个在线来源归成「设备键」，再交给 aliveRows 取哈希。
//
// 与 pdnd 的 core.DeviceKey（pdnd/core/device_key.go）同一口径，用例表也同一份
// （alive_device_key_test.go 直接读 pdnd 的 TestDeviceKey 表对照）：
//   - IPv4 与 IPv4 映射地址（::ffff:a.b.c.d）按单个 IPv4 地址；
//   - IPv6 去掉 zone 后取 /64 网段，写成 "2001:db8:1:2::/64"（用户 10-09 定：运营商
//     给一户分一个 /64，隐私扩展地址在其中随时轮换）；
//   - 已经是 "前缀/64" 的串（新节点上报的就是它）和解析不了的串原样保留。
//
// 在面板侧再归一一次，是为了新旧节点混跑（灰度、回滚）时口径一致：没升级的节点报
// 原始 IPv6 地址，新节点报 /64 键，只对上报串取哈希的话同一台设备会算成两个，
// 严格模式下多算一台就可能把整条订阅摘出下发名单。
func aliveDeviceKey(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}
