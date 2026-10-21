package service

// 同机端口门禁与保留端口（同机端口先到先得）。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/protocol"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// nodePortRange 是闭区间 [From, To]。
type nodePortRange struct{ From, To int }

func (r nodePortRange) contains(port int) bool { return port >= r.From && port <= r.To }

// nodePortPolicy 是节点端口的保留表。
//
//   - Reserved：任何服务器都拒绝。22（SSH）、25（SMTP）、53（DNS）、80（预留给证书 HTTP-01 验证）。
//   - PanelReserved：面板自己的端口，只有节点所在服务器就是面板机时才拒绝；判断不了时只给警告。
//   - PanelHosts：面板机的地址（域名或 IP，已规整）。服务器或节点任一地址命中，或是回环地址，即判为面板机；
//     其中只要有 IP 字面量，而服务器报过的 IP 都不在表里，就判为「不是面板机」。
//
// 这张表目前不可配置，用缺省值（要可配再在 Service 上加注入）。
type nodePortPolicy struct {
	Reserved      []nodePortRange
	PanelReserved []nodePortRange
	PanelHosts    []string
}

// defaultNodePortPolicy 是缺省的保留表。
var defaultNodePortPolicy = nodePortPolicy{
	Reserved:      []nodePortRange{{22, 22}, {25, 25}, {53, 53}, {80, 80}},
	PanelReserved: []nodePortRange{{80, 80}, {443, 443}, {5432, 5432}, {6379, 6379}, {9000, 9003}, {9080, 9080}, {9443, 9443}},
}

// nodePortNames 是常见保留端口的用途，只用于文案。
var nodePortNames = map[int]string{
	22: "SSH", 25: "SMTP", 53: "DNS", 80: "HTTP，预留给证书 HTTP-01 验证",
	443: "面板 HTTPS", 5432: "PostgreSQL", 6379: "Redis",
	9000: "面板网关", 9001: "面板网关", 9002: "面板网关", 9003: "面板网关",
	9080: "面板本机运维入口", 9443: "节点连面板的 gRPC",
}

func nodePortPurpose(port int) string {
	if name, ok := nodePortNames[port]; ok {
		return "（" + name + "）"
	}
	return ""
}

// nodePanelHostState 是「节点所在服务器是不是面板机」的判断结果。
type nodePanelHostState int

const (
	nodePanelHostUnknown nodePanelHostState = iota
	nodePanelHostYes
	nodePanelHostNo
)

// nodeNormalizeHostAddr 把地址规整成比较用的形式：IP 取规范写法，域名小写去掉结尾的点。
func nodeNormalizeHostAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(raw), ".")
}

// panelHost 判断一组地址（节点的连接地址、服务器的主机名与各 IP）是不是面板机。
func (p nodePortPolicy) panelHost(addrs []string) nodePanelHostState {
	panel := map[string]bool{}
	panelHasIP := false
	for _, h := range p.PanelHosts {
		h = nodeNormalizeHostAddr(h)
		panel[h] = true
		if net.ParseIP(h) != nil {
			panelHasIP = true
		}
	}
	sawIP := false
	for _, raw := range addrs {
		a := nodeNormalizeHostAddr(raw)
		if a == "" {
			continue
		}
		if a == "localhost" || panel[a] {
			return nodePanelHostYes
		}
		if ip := net.ParseIP(a); ip != nil {
			if ip.IsLoopback() {
				return nodePanelHostYes
			}
			sawIP = true
		}
	}
	if panelHasIP && sawIP {
		return nodePanelHostNo
	}
	return nodePanelHostUnknown
}

// checkReserved 按保留表判断端口：命中 Reserved 或（面板机上的）PanelReserved 回 422，
// 判断不了是不是面板机时命中 PanelReserved 只返回警告。
func (p nodePortPolicy) checkReserved(port int, host nodePanelHostState) (string, error) {
	for _, r := range p.Reserved {
		if r.contains(port) {
			return "", inventoryInvalid("server_port",
				fmt.Sprintf("端口 %d%s 是保留端口，不能给节点使用", port, nodePortPurpose(port)))
		}
	}
	for _, r := range p.PanelReserved {
		if !r.contains(port) {
			continue
		}
		switch host {
		case nodePanelHostYes:
			return "", inventoryInvalid("server_port",
				fmt.Sprintf("端口 %d%s 是面板自己的端口，这台服务器就是面板机，不能给节点使用", port, nodePortPurpose(port)))
		case nodePanelHostUnknown:
			return fmt.Sprintf("端口 %d%s 是面板自己的端口：如果这台服务器同时运行面板，节点会和面板抢端口", port, nodePortPurpose(port)), nil
		}
	}
	return "", nil
}

// nodePortClaim 是一次同机端口门禁要查的内容。
type nodePortClaim struct {
	ServerID uuid.UUID
	// Exclude 是正在改、迁移的节点自己；新建、复制为 nil
	Exclude *uuid.UUID
	Port    int
	L4      string
	// Host 是节点对外地址，一并参与「是不是面板机」判断
	Host string
	// CheckReserved 为 false 时只查同机冲突（改节点时端口没变）
	CheckReserved bool
}

// claimNodePort 在调用方已持服务器行锁时检查端口：先保留表，再查同一服务器同一端口上 L4 冲突（protocol.L4Conflicts：相等，或一方是 tcp+udp）的未退役节点。
// 端口未配置（<=0）直接通过。返回只提示、不拒绝的说明（可能为 nil）。
func (s *Service) claimNodePort(ctx context.Context, srv model.Server, c nodePortClaim) ([]string, error) {
	if c.Port <= 0 {
		return nil, nil
	}
	var warnings []string
	if c.CheckReserved {
		addrs := []string{c.Host}
		for _, a := range []*string{srv.Hostname, srv.PublicIPv4, srv.PublicIPv6, srv.PrivateIPv4} {
			if a != nil {
				addrs = append(addrs, *a)
			}
		}
		policy := defaultNodePortPolicy
		warning, err := policy.checkReserved(c.Port, policy.panelHost(addrs))
		if err != nil {
			return nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	holder, taken, err := s.repo.NodePortHolder(ctx, c.ServerID, c.Port, protocol.ConflictingL4s(c.L4), c.Exclude)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, nodePortConflict(c.Port, c.L4, holder)
	}
	return warnings, nil
}

// nodePortConflict 是同机端口被占用的 409；holder 为空时（库里唯一索引兜底）不写名字。
func nodePortConflict(port int, l4, holder string) error {
	label := protocol.PortClaimLabel(port, l4)
	var head string
	if holder != "" {
		head = fmt.Sprintf("端口 %s 已被同一服务器上的节点「%s」占用", label, holder)
	} else {
		head = fmt.Sprintf("端口 %s 已被同一服务器上的其他节点占用", label)
	}
	return &errs.Error{Code: errs.Conflict, Message: head + "，换一个端口或先退役占用的节点",
		Fields: map[string]string{"server_port": head}}
}

// nodeWriteErr 把 repo 写节点的错误译成对外错误。
func nodeWriteErr(err error, port int, l4 string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, repo.ErrListenClaimTaken):
		return nodePortConflict(port, l4, "")
	case errors.Is(err, repo.ErrNodeNameTaken):
		return &errs.Error{Code: errs.Conflict, Message: "节点名称已存在", Fields: map[string]string{"name": "节点名称已存在"}}
	case errors.Is(err, repo.ErrInventoryCheck):
		return inventoryConflict("节点数据不满足约束，这次修改没有生效")
	}
	return err
}
