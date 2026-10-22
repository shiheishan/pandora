package service

import (
	"maps"
	"net"
	"strings"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

// relayRenderNode 由父节点线路派生一条中转线路：Name/Host/Port 换成中转的；Config 浅拷贝一份顶层 map 再改（父节点的 Config
// 被缓存共享，绝不能原地改）；ECHConfigList、TrafficRate 与父节点共用同一份（只读）。
// SNI：中转给了 sni 时，REALITY（Config["security"]=="reality"）写 server_names = []any{sni}，其他写 server_name = sni；
// 没给时，非 REALITY 且父节点 Config 没有 server_name：若父节点是 ACME 证书，写 server_name = 规整后的父节点地址（IP 亦补，客户端连中转机须按父节点证书的 IP SAN 握手）；
// 否则若父节点地址是域名（net.ParseIP == nil）写 server_name = 父节点地址（客户端连中转机，TLS 仍要按父节点的域名握手）。
// Host：中转给了 host_header 写 host = 它；没给且父节点 Config 没有 host 时写 host = 父节点地址（渲染器缺省会用 Node.Host，而那已是中转地址）。
func relayRenderNode(parent render.Node, rel repo.DeliveryRelay) render.Node {
	cfg := maps.Clone(parent.Config)
	if cfg == nil {
		cfg = make(map[string]any)
	}

	isReality := false
	if sec, ok := cfg["security"].(string); ok && strings.EqualFold(strings.TrimSpace(sec), "reality") {
		isReality = true
	}

	if rel.SNI != nil && *rel.SNI != "" {
		sni := *rel.SNI
		if isReality {
			cfg["server_names"] = []any{sni}
		} else {
			cfg["server_name"] = sni
		}
	} else if !isReality {
		if protocol.CertMode(parent.Type, cfg) == protocol.CertModeACME && cfgEmpty(cfg, "server_name") {
			cfg["server_name"] = protocol.NormalizeCertIdentifier(parent.Host)
		} else if cfgEmpty(cfg, "server_name") && net.ParseIP(parent.Host) == nil {
			cfg["server_name"] = parent.Host
		}
	}

	if rel.HostHeader != nil && *rel.HostHeader != "" {
		cfg["host"] = *rel.HostHeader
	} else {
		if cfgEmpty(cfg, "host") {
			cfg["host"] = parent.Host
		}
	}

	return render.Node{
		Name:          rel.Name,
		Type:          parent.Type,
		Host:          rel.Host,
		Port:          rel.Port,
		Config:        cfg,
		TrafficRate:   parent.TrafficRate,
		ECHConfigList: parent.ECHConfigList,
	}
}

// cfgEmpty 报告 cfg[key] 缺席、不是字符串或只有空白（渲染器按同样的口径当作没配）。
func cfgEmpty(cfg map[string]any, key string) bool {
	v, _ := cfg[key].(string)
	return strings.TrimSpace(v) == ""
}
