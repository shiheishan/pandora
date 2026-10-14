package protocol

import (
	"encoding/json"
	"strings"
)

// ============================================================
//  节点入站的 L4 协议：同机端口门禁的键
// ============================================================
//
// 同一台服务器上两个节点配到同一个 (端口, L4)，pdnd 只能让先登记的那个起来
// （kernel/port_claims.go，先到先得），后来的那个不启动、用户全部连不上。面板在
// 建、改、克隆、迁移节点时就按 (服务器, 端口, L4) 拦下，
// 库里 nodes 的唯一索引 (server_id, server_port, listen_l4) 兜底。TCP 与 UDP 分开算：同一个端口号可以 TCP、UDP
// 各被一个节点占用（例如 REALITY 走 TCP、hy2 走 UDP），这是合法用法。

// listenL4 推导节点入站实际 bind 的 L4 协议，与 pdnd kernel/port_claims.go 的
// inboundPortKey 同口径（port_l4_test.go 读 pdnd 的用例表对照）。库里的 nodes.listen_l4
// 不是生成列，由调用方用这里的结果写入。
//
// 规则：hysteria2 / hy2 / tuic / juicity 走 UDP；vless / vmess 的 network 为 xhttp-h3
// 或 mKCP 走 UDP；trojan 的 mKCP 走 UDP；shadowsocks / ss 只有 network=udp 时走 UDP；
// mieru 看 transport（大小写不敏感，缺省 TCP）；其余（anytls、socks、http、naive、
// shadowtls 与上述的 TCP 传输）走 TCP。SOCKS 的 UDP ASSOCIATE 用临时端口，不占入站端口。
//
// raw 是库里存的 protocol_config（xboard 形状；network、transport 两边同名）。只认字符串
// 值：数字、布尔落不进任何 UDP 分支。去首尾空白只去空格（同 SQL btrim 的缺省口径）。
func listenL4(nodeType string, raw json.RawMessage) string {
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg) // 不是对象就当没有字段
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
	return ""
}
