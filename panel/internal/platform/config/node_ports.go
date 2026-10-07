package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

//------------------------------------------------------------------------------
// 节点端口保留表（w4deliver）：后台建、改、复制、迁移节点时按它拒绝或提示
//------------------------------------------------------------------------------

const (
	// defaultNodeReservedPorts 任何服务器上都不给节点用：SSH、SMTP、DNS，以及 80
	// （预留给证书 HTTP-01 验证）。
	defaultNodeReservedPorts = "22,25,53,80"
	// defaultPanelReservedPorts 是面板自己的端口：节点所在服务器就是面板机时拒绝，
	// 判断不了时只提示（nginx 80/443、PostgreSQL、Valkey、三个网关与 pprof 段）。
	defaultPanelReservedPorts = "80,443,5432,6379,9000-9003"
)

// PortRange 是闭区间 [From, To]。
type PortRange struct{ From, To int }

// NodePorts 是节点端口的保留表。
type NodePorts struct {
	// Reserved 在任何服务器上都拒绝（AEGIS_NODE_RESERVED_PORTS，缺省 22,25,53,80）
	Reserved []PortRange
	// PanelReserved 只在面板机上拒绝，判断不了时提示（AEGIS_PANEL_RESERVED_PORTS，
	// 缺省 80,443,5432,6379,9000-9003）
	PanelReserved []PortRange
	// PanelHosts 是面板机的地址（AEGIS_PANEL_HOSTS，逗号分隔的域名或 IP），外加
	// AEGIS_PUBLIC_BASE_URL 的主机名。写上面板机的公网 IP 后，IP 对不上的服务器
	// 不再提示面板端口。
	PanelHosts []string
}

// loadNodePorts 读三项；写了但格式不对就拒绝启动，免得保留表悄悄失效。
// 任一端口表写成 none 表示清空。
func loadNodePorts(publicBaseURL string) (NodePorts, error) {
	var out NodePorts
	var err error
	if out.Reserved, err = parsePortRanges("AEGIS_NODE_RESERVED_PORTS",
		trimmedEnv("AEGIS_NODE_RESERVED_PORTS", defaultNodeReservedPorts)); err != nil {
		return out, err
	}
	if out.PanelReserved, err = parsePortRanges("AEGIS_PANEL_RESERVED_PORTS",
		trimmedEnv("AEGIS_PANEL_RESERVED_PORTS", defaultPanelReservedPorts)); err != nil {
		return out, err
	}
	if u, perr := url.Parse(strings.TrimSpace(publicBaseURL)); perr == nil && u.Hostname() != "" {
		out.PanelHosts = append(out.PanelHosts, strings.ToLower(u.Hostname()))
	}
	for _, h := range strings.Split(trimmedEnv("AEGIS_PANEL_HOSTS", ""), ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			if strings.ContainsAny(h, " /\\\"'`$") {
				return out, fmt.Errorf("AEGIS_PANEL_HOSTS 只能是逗号分隔的域名或 IP，%q 不合法", h)
			}
			out.PanelHosts = append(out.PanelHosts, h)
		}
	}
	return out, nil
}

// parsePortRanges 解析「22,25,9000-9003」这样的端口表；none 表示空表。
func parsePortRanges(name, raw string) ([]PortRange, error) {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "none") {
		return []PortRange{}, nil
	}
	var out []PortRange
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		from, err1 := strconv.Atoi(strings.TrimSpace(lo))
		to := from
		var err2 error
		if isRange {
			to, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || from < 1 || to > 65535 || from > to {
			return nil, fmt.Errorf("%s 的 %q 不是 1–65535 的端口或「起-止」区间", name, part)
		}
		out = append(out, PortRange{From: from, To: to})
	}
	return out, nil
}

// DefaultNodePortRanges 返回两张缺省端口表（api/admin 的测试拿它对照 nodefabric 的缺省表）。
func DefaultNodePortRanges() (reserved, panelReserved []PortRange) {
	reserved, _ = parsePortRanges("default", defaultNodeReservedPorts)
	panelReserved, _ = parsePortRanges("default", defaultPanelReservedPorts)
	return reserved, panelReserved
}
