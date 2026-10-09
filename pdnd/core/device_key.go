package core

import "net/netip"

// DeviceKey 把来源地址归成「设备」的键，供设备数限制与在线上报共用（NativeCore 的
// 各适配器与 mieru 的在线记录同一口径）：
//   - IPv4 与 IPv4 映射地址（双栈监听上的 ::ffff:a.b.c.d）按单个 IPv4 地址；
//   - IPv6 去掉 zone 后取 /64 网段，写成 "2001:db8:1:2::/64"（用户 10-09 定：运营商
//     给一户分一个 /64，隐私扩展地址在其中随时轮换，按单个地址会把一台设备算成好几台）；
//   - 解析不了的（如 "unknown"）原样作键。
//
// 上报给面板的就是这个键：面板对上报串取哈希、按订阅去重计设备数，节点与面板因此
// 同一口径，跨节点也按同一个键去重。
func DeviceKey(ip string) string {
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
