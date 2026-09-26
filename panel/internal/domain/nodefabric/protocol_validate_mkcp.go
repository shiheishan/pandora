// [INPUT]: 依赖标准库 encoding/json
// [OUTPUT]: 包内提供 validateMKCPConfig、validateMKCPMask 与 MTU 边界常量
// [POS]: domain/nodefabric 协议校验的 mKCP 分项：从 protocol_schema.go 拆出。MTU 边界与掩码开销和节点端 kernel/mkcp_transport.go 保持一致，两边对不上会出现「面板存进去了、节点起不来」
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mKCP 的 MTU 边界。和节点端 kernel/mkcp_transport.go 里的同名常量
// 保持一致——两边对不上，就会出现「面板存进去了、节点起不来」。
//
// 上限 1460 是 UDP 载荷的上限：以太网 1500 减去 IPv4 20 + UDP 8。
// 下限 576 是 IPv4 要求的最小重组缓冲，再小没有实际意义。
const (
	mkcpMinMTU = 576
	mkcpMaxMTU = 1460
)

// validateMKCPConfig 校验 mKCP 的调优参数。
//
// 这些值最终由节点端的 ParseMKCPConfig 兜底，但那时已经晚了——配错的
// 节点要等到启动失败才发现，而管理员看到的只是「节点不健康」。在这里
// 拦住，他还站在表单前面，能立刻改。
//
// 边界跟节点端保持一致，两边对不上会出现「面板存进去了、节点起不来」
// 这种最难查的状态。
func validateMKCPConfig(fields map[string]string, network string,
	mtu, tti, uplink, downlink, readBuf, writeBuf json.RawMessage) {

	// 不是 mKCP 就不该出现这些字段：多半是从别的传输的配置里复制粘贴
	// 带过来的，留着会让人以为它们生效了。
	isMKCP := containsString([]string{"mkcp", "kcp", "m-kcp"},
		strings.ToLower(strings.TrimSpace(network)))
	if !isMKCP {
		for name, v := range map[string]json.RawMessage{
			"mtu": mtu, "tti": tti, "uplink_capacity": uplink,
			"downlink_capacity": downlink,
			"read_buffer_size":  readBuf, "write_buffer_size": writeBuf,
		} {
			if len(v) > 0 {
				fields["protocol_config."+name] = name + " 只在 network=mkcp 时有效"
			}
		}
		return
	}

	checkRange := func(name string, v json.RawMessage, lo, hi int) {
		if len(v) == 0 {
			return
		}
		n, ok := rawJSONInt(v)
		if !ok {
			fields["protocol_config."+name] = name + " 必须是整数"
			return
		}
		if n < lo || n > hi {
			fields["protocol_config."+name] = fmt.Sprintf("%s 应在 %d 到 %d 之间", name, lo, hi)
		}
	}
	checkRange("mtu", mtu, mkcpMinMTU, mkcpMaxMTU)
	// TTI 是发送周期，太长重传迟钝，太短纯烧 CPU。
	checkRange("tti", tti, 10, 100)
	// 带宽单位 MB/s，上限给到万兆。
	checkRange("uplink_capacity", uplink, 1, 1000)
	checkRange("downlink_capacity", downlink, 1, 1000)
	// 缓冲区单位 MB。
	checkRange("read_buffer_size", readBuf, 1, 64)
	checkRange("write_buffer_size", writeBuf, 1, 64)
}

// rawJSONInt 把一段 JSON 数字解成整数。
func rawJSONInt(v json.RawMessage) (int, bool) {
	var n json.Number
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// mKCP 掩码的每包开销。和节点端 udpmask 的实现保持一致——两边对不上会
// 出现「面板存进去了、节点起不来」这种最难查的状态。
var mkcpMaskOverhead = map[string]int{
	"":               0,
	"none":           0,
	"mkcp-original":  0,  // 上游给「不加掩码」起的名字
	"mkcp-aes128gcm": 28, // 12 字节 nonce + 16 字节 tag
}

// validateMKCPMask 校验掩码设置。
func validateMKCPMask(fields map[string]string, network, mask, password string, mtu json.RawMessage) {
	mask = strings.ToLower(strings.TrimSpace(mask))

	isMKCP := containsString([]string{"mkcp", "kcp", "m-kcp"},
		strings.ToLower(strings.TrimSpace(network)))
	if !isMKCP {
		if mask != "" || password != "" {
			fields["protocol_config.mask"] = "mask 只在 network=mkcp 时有效"
		}
		return
	}

	overhead, known := mkcpMaskOverhead[mask]
	if !known {
		// 不认识的类型必须拒绝。放过去的话节点会裸奔，而管理员以为它
		// 加着密——这比不加密更糟，因为它给了错误的安全感。
		fields["protocol_config.mask"] = "只支持 none 或 mkcp-aes128gcm"
		return
	}

	if overhead > 0 && strings.TrimSpace(password) == "" {
		// 空密码时密钥退化成 sha256("")，谁都算得出来，那这层就只是
		// 障眼法而不是加密。
		fields["protocol_config.mask_password"] = "启用 mkcp-aes128gcm 必须设置密码"
		return
	}
	if overhead == 0 && strings.TrimSpace(password) != "" {
		fields["protocol_config.mask_password"] = "没有启用加密掩码时不应填密码"
		return
	}

	// MTU 要给掩码开销留出空间。mkcpMaxMTU 是 UDP 载荷上限，已经扣过
	// IP + UDP 头；掩码开销也加在载荷里，顶着上限再套掩码，发出去的
	// 以太网帧就超长了，会被 IP 分片。分片的 UDP 在不少网络上直接被丢，
	// 症状是「小包能通、大包不通」。
	if overhead > 0 && len(mtu) > 0 {
		if n, ok := rawJSONInt(mtu); ok && n > mkcpMaxMTU-overhead {
			fields["protocol_config.mtu"] = fmt.Sprintf(
				"启用 %s 后 mtu 不能超过 %d（掩码每包多占 %d 字节，再大会被 IP 分片）",
				mask, mkcpMaxMTU-overhead, overhead)
		}
	}
}
