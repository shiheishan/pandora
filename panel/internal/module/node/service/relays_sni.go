package service

// 中转 SNI 与父节点 TLS 配置的对照：REALITY 父节点的 SNI 覆盖必须是父节点 server_names 之一（否则握手必然失败）；
// 父节点地址是 IP、开了 TLS 又没配 server_name 时，中转不填 SNI 就会按中转地址握手，后台给一句提示。
// 口径与 relayRenderNode（node_source_relay.go）一致：判定都按翻译后的内核配置。

import (
	"fmt"
	"net"
	"slices"
	"strings"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
)

// relayParentTLS 是父节点与中转 SNI 有关的 TLS 配置。
type relayParentTLS struct {
	reality     bool
	serverNames []string // 只在 reality 时有意义
	// needSNI：父节点地址是 IP、开了 TLS、没配 server_name，中转不填 SNI 会按中转地址握手
	needSNI bool
}

func relayParentTLSOf(parent model.Node) relayParentTLS {
	kernel := nodeKernelConfig(parent)
	var out relayParentTLS
	if sec, _ := kernel["security"].(string); strings.EqualFold(strings.TrimSpace(sec), "reality") {
		out.reality = true
		out.serverNames = kernelStrings(kernel["server_names"])
		return out
	}
	tlsOn, _ := kernel["tls"].(bool)
	if cert, _ := kernel["cert_path"].(string); strings.TrimSpace(cert) != "" {
		tlsOn = true
	}
	switch protocol.CanonicalType(nodeStr(parent.NodeType)) {
	case "hysteria2", "tuic", "juicity":
		tlsOn = true
	}
	out.needSNI = tlsOn && cfgEmpty(kernel, "server_name") && net.ParseIP(strings.TrimSpace(nodeStr(parent.ServerHost))) != nil
	return out
}

// kernelStrings 把内核配置里的字符串数组（[]string 或 JSON 解出的 []any）取成 []string，非字符串项丢弃。
func kernelStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(x) != "" {
			return []string{x}
		}
	}
	return nil
}

// relayCheckSNI 校验中转的 SNI 覆盖与父节点是否对得上：REALITY 父节点只允许填父节点 server_names 之一（422 sni）。
// sni 为 nil 表示不覆盖，沿用父节点，总是允许。
func relayCheckSNI(parent model.Node, sni *string) error {
	if sni == nil {
		return nil
	}
	p := relayParentTLSOf(parent)
	if p.reality && !slices.Contains(p.serverNames, *sni) {
		return inventoryInvalid("sni", fmt.Sprintf("父节点是 REALITY，SNI 只能填父节点 server_names 之一（%s），或留空沿用父节点",
			strings.Join(p.serverNames, "、")))
	}
	return nil
}

// relaySNIHint 是后台中转页的 SNI 提示；没有要提示的返回空串。
func relaySNIHint(parent model.Node) string {
	p := relayParentTLSOf(parent)
	switch {
	case p.reality:
		return fmt.Sprintf("父节点是 REALITY：SNI 只能填父节点 server_names 之一（%s），留空沿用父节点。", strings.Join(p.serverNames, "、"))
	case p.needSNI:
		return "父节点地址是 IP 且没配 server_name：中转不填 SNI 时会按中转地址握手，请给每条中转填 SNI。"
	}
	return ""
}
