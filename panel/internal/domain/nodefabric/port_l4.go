package nodefabric

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ============================================================
//  节点入站的 L4 协议：同机端口门禁的键
// ============================================================
//
// 同一台服务器上两个节点配到同一个 (端口, L4)，pdnd 只能让先登记的那个起来
// （kernel/port_claims.go，先到先得），后来的那个不启动、用户全部连不上。面板在
// 建、改、克隆、迁移节点时就按 (服务器, 端口, L4) 拦下（checkNodePortClaim），
// 迁移 00122 的唯一部分索引兜底。TCP 与 UDP 分开算：同一个端口号可以 TCP、UDP
// 各被一个节点占用（例如 REALITY 走 TCP、hy2 走 UDP），这是合法用法。

const (
	ListenTCP = "tcp"
	ListenUDP = "udp"
)

// ListenL4 推导节点入站实际 bind 的 L4 协议，规则三处同口径：
//   - 迁移 00122 的生成列 nodes.listen_l4（PG18 用例逐项对照）；
//   - pdnd kernel/port_claims.go 的 inboundPortKey（port_l4_test.go 读 pdnd 的用例表对照）；
//   - 这里。
//
// 规则：hysteria2 / hy2 / tuic / juicity 走 UDP；vless / vmess 的 network 为 xhttp-h3
// 或 mKCP 走 UDP；trojan 的 mKCP 走 UDP；shadowsocks / ss 只有 network=udp 时走 UDP；
// mieru 看 transport（大小写不敏感，缺省 TCP）；其余（anytls、socks、http、naive、
// shadowtls 与上述的 TCP 传输）走 TCP。SOCKS 的 UDP ASSOCIATE 用临时端口，不占入站端口。
//
// raw 是库里存的 protocol_config（xboard 形状；network、transport 两边同名）。只认字符串
// 值：与 SQL 的 ->> 对非字符串值取出的文本（数字、布尔）一样，都落不进任何 UDP 分支。
// 去首尾空白只去空格，与 SQL 的 btrim 一致。
func ListenL4(nodeType string, raw json.RawMessage) string {
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg) // 不是对象就当没有字段，与 SQL 的 ->> 取不到值一致
	network := strings.ToLower(strings.Trim(stringField(cfg, "network"), " "))
	switch strings.ToLower(strings.Trim(nodeType, " ")) {
	case "hysteria2", "hy2", "tuic", "juicity":
		return ListenUDP
	case "vless", "vmess":
		if network == "xhttp-h3" || isMKCPNetworkName(network) {
			return ListenUDP
		}
	case "trojan":
		if isMKCPNetworkName(network) {
			return ListenUDP
		}
	case "shadowsocks", "ss":
		if network == "udp" {
			return ListenUDP
		}
	case "mieru":
		if strings.ToLower(stringField(cfg, "transport")) == "udp" {
			return ListenUDP
		}
	}
	return ListenTCP
}

// isMKCPNetworkName 与 pdnd kernel/mkcp_transport.go 的 isMKCPNetwork 认同一组写法。
func isMKCPNetworkName(network string) bool {
	switch network {
	case "mkcp", "kcp", "m-kcp":
		return true
	}
	return false
}

func stringField(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	// SQL 的 ->> 对数字、布尔取出文本；这些文本不会命中任何 UDP 分支，这里返回空串结果相同
	return ""
}

// PortClaimLabel 是给人看的「端口/L4」写法，与 pdnd 冲突文案一致：443/TCP。
func PortClaimLabel(port int, l4 string) string {
	return strconv.Itoa(port) + "/" + strings.ToUpper(l4)
}
