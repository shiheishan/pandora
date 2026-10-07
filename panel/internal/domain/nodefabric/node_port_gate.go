package nodefabric

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ============================================================
//  同机端口门禁与保留端口（用户 2026-10-07 定：同机端口先到先得）
// ============================================================

// PortRange 是闭区间 [From, To]。
type PortRange struct{ From, To int }

func (r PortRange) contains(port int) bool { return port >= r.From && port <= r.To }

// PortPolicy 是节点端口的保留表，经 platform/config 配置（api/admin 装配时注入）。
//
//   - Reserved：任何服务器都拒绝。缺省 22（SSH）、25（SMTP）、53（DNS）、80（预留给证书
//     HTTP-01 验证）。
//   - PanelReserved：面板自己的端口，只有节点所在服务器就是面板机时才拒绝；判断不了是不是
//     面板机时只给警告，不拒绝。缺省 80、443、5432、6379、9000–9003。
//   - PanelHosts：面板机的地址（域名或 IP，小写、IP 已规范化）。服务器或节点的任一地址命中，
//     或是回环地址，即判为面板机。其中只要有 IP 字面量，而服务器报过的 IP 都不在表里，就判为
//     「不是面板机」，不再提示。
type PortPolicy struct {
	Reserved      []PortRange
	PanelReserved []PortRange
	PanelHosts    []string
}

// DefaultPortPolicy 与 platform/config 的缺省值相同（api/admin 的测试对照两边）。
func DefaultPortPolicy() PortPolicy {
	return PortPolicy{
		Reserved:      []PortRange{{22, 22}, {25, 25}, {53, 53}, {80, 80}},
		PanelReserved: []PortRange{{80, 80}, {443, 443}, {5432, 5432}, {6379, 6379}, {9000, 9003}},
	}
}

// SetPortPolicy 注入保留端口表；不调用时用 DefaultPortPolicy。只在进程装配时调用一次。
func (s *Service) SetPortPolicy(p PortPolicy) {
	hosts := make([]string, 0, len(p.PanelHosts))
	for _, h := range p.PanelHosts {
		if h = normalizeHostAddr(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	p.PanelHosts = hosts
	s.ports = &p
}

func (s *Service) portPolicy() PortPolicy {
	if s.ports == nil {
		return DefaultPortPolicy()
	}
	return *s.ports
}

// portNames 是常见保留端口的用途，只用于文案。
var portNames = map[int]string{
	22: "SSH", 25: "SMTP", 53: "DNS", 80: "HTTP，预留给证书 HTTP-01 验证",
	443: "面板 HTTPS", 5432: "PostgreSQL", 6379: "Valkey",
	9000: "面板网关", 9001: "面板网关", 9002: "面板网关", 9003: "面板网关",
}

func portPurpose(port int) string {
	if name, ok := portNames[port]; ok {
		return "（" + name + "）"
	}
	return ""
}

// panelHostState 是「节点所在服务器是不是面板机」的判断结果。
type panelHostState int

const (
	panelHostUnknown panelHostState = iota
	panelHostYes
	panelHostNo
)

// normalizeHostAddr 把地址规整成比较用的形式：IP 取规范写法，域名小写去掉结尾的点。
func normalizeHostAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(raw), ".")
}

// panelHost 判断一组地址（节点的连接地址、服务器的主机名与各 IP）是不是面板机。
func (p PortPolicy) panelHost(addrs []string) panelHostState {
	panel := map[string]bool{}
	panelHasIP := false
	for _, h := range p.PanelHosts {
		panel[h] = true
		if net.ParseIP(h) != nil {
			panelHasIP = true
		}
	}
	sawIP := false
	for _, raw := range addrs {
		a := normalizeHostAddr(raw)
		if a == "" {
			continue
		}
		if a == "localhost" || panel[a] {
			return panelHostYes
		}
		if ip := net.ParseIP(a); ip != nil {
			if ip.IsLoopback() {
				return panelHostYes
			}
			sawIP = true
		}
	}
	if panelHasIP && sawIP {
		return panelHostNo
	}
	return panelHostUnknown
}

// checkReservedPort 按保留表判断端口：命中 Reserved 或（面板机上的）PanelReserved 回 422，
// 判断不了是不是面板机时命中 PanelReserved 只返回警告。
func (p PortPolicy) checkReservedPort(port int, host panelHostState) (warning string, err error) {
	for _, r := range p.Reserved {
		if r.contains(port) {
			return "", httpx.Invalid(map[string]string{
				"server_port": fmt.Sprintf("端口 %d%s 是保留端口，不能给节点使用", port, portPurpose(port))})
		}
	}
	for _, r := range p.PanelReserved {
		if !r.contains(port) {
			continue
		}
		switch host {
		case panelHostYes:
			return "", httpx.Invalid(map[string]string{
				"server_port": fmt.Sprintf("端口 %d%s 是面板自己的端口，这台服务器就是面板机，不能给节点使用", port, portPurpose(port))})
		case panelHostUnknown:
			return fmt.Sprintf("端口 %d%s 是面板自己的端口：如果这台服务器同时运行面板，节点会和面板抢端口", port, portPurpose(port)), nil
		}
	}
	return "", nil
}

// serverAddrsTx 取服务器的主机名与各 IP（判断是不是面板机用）。调用方已持服务器行锁。
func serverAddrsTx(ctx context.Context, tx pgx.Tx, tenantID, serverID string) ([]string, error) {
	var hostname, v4, v6, private *string
	err := tx.QueryRow(ctx, `SELECT s.hostname, host(s.public_ipv4), host(s.public_ipv6), host(s.private_ipv4)
		  FROM servers s WHERE s.tenant_id=$1 AND s.id=$2::uuid`, tenantID, serverID).Scan(&hostname, &v4, &v6, &private)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []string{value(hostname), value(v4), value(v6), value(private)}, nil
}

// nodePortClaim 是一次端口门禁要检查的内容。
type nodePortClaim struct {
	ServerID string
	// ExcludeNodeID 是正在改的节点自己（新建、复制时为空）
	ExcludeNodeID string
	Port          int
	L4            string
	// Host 是节点对外的连接地址，一并参与「是不是面板机」的判断
	Host string
	// CheckReserved 为 false 时只查同机冲突、不查保留表（改节点时端口没变）
	CheckReserved bool
}

// checkNodePortClaim 在调用方已持有的服务器行锁内（lockServerCapacity / lockServerRow）
// 检查端口：先保留表，再同一服务器上未退役节点的 (端口, L4)。冲突回 409，文案写明占用者
// 的节点名；保留端口回 422；判断不了是不是面板机时返回警告。端口为 0（未配置）不检查。
func (s *Service) checkNodePortClaim(ctx context.Context, tx pgx.Tx, tenantID string, c nodePortClaim) ([]string, error) {
	if c.ServerID == "" || c.Port <= 0 {
		return nil, nil
	}
	var warnings []string
	if c.CheckReserved {
		addrs, err := serverAddrsTx(ctx, tx, tenantID, c.ServerID)
		if err != nil {
			return nil, err
		}
		policy := s.portPolicy()
		warning, err := policy.checkReservedPort(c.Port, policy.panelHost(append(addrs, c.Host)))
		if err != nil {
			return nil, err
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	var holderID, holderName string
	err := tx.QueryRow(ctx, `SELECT n.id::text, n.name FROM nodes n
		 WHERE n.tenant_id=$1 AND n.server_id=$2::uuid AND n.server_port=$3 AND n.listen_l4=$4
		   AND n.id IS DISTINCT FROM nullif($5,'')::uuid
		   AND n.status NOT IN ('retired','destroyed') AND n.serving_status<>'retired'
		 ORDER BY n.created_at, n.id LIMIT 1`,
		tenantID, c.ServerID, c.Port, c.L4, c.ExcludeNodeID).Scan(&holderID, &holderName)
	if errors.Is(err, pgx.ErrNoRows) {
		return warnings, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, portClaimConflict(c.Port, c.L4, holderName)
}

func portClaimConflict(port int, l4, holder string) error {
	label := PortClaimLabel(port, l4)
	msg := "端口 " + label + " 已被同一服务器上的其他节点占用"
	if holder != "" {
		msg = "端口 " + label + " 已被同一服务器上的节点「" + holder + "」占用"
	}
	return &httpx.Error{Code: httpx.CodeConflict, Message: msg + "，换一个端口或先退役占用的节点",
		Fields: map[string]string{"server_port": msg}}
}

// nodeListenClaimIndex 是迁移 00122 的唯一部分索引名：服务器行锁之外的写撞上它时，
// 同样译成端口冲突，而不是「节点名称已存在」。
const nodeListenClaimIndex = "nodes_listen_claim_unique"

// nodeUniqueViolation 把写 nodes 时的唯一约束冲突译成中文：端口索引是端口冲突，其余是重名。
// 不是唯一约束冲突时原样返回 nil。
func nodeUniqueViolation(err error, port int, l4 string) error {
	if !db.IsUniqueViolation(err) {
		return nil
	}
	if db.ConstraintName(err) == nodeListenClaimIndex {
		return portClaimConflict(port, l4, "")
	}
	return httpx.New(httpx.CodeConflict, "节点名称已存在")
}

// lockServerRow 给节点所在服务器加行锁（改节点端口时）。与 lockServerCapacity 是同一把锁，
// 只是不查容量与状态：改端口不新增节点。服务器已删除时不锁、不报错，端口检查照常按
// server_id 查。
func lockServerRow(ctx context.Context, tx pgx.Tx, tenantID, serverID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT s.id::text FROM servers s
		 WHERE s.tenant_id=$1 AND s.id=$2::uuid FOR UPDATE`, tenantID, serverID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}

// PortPolicyOf 返回 Service 当前生效的保留端口表（装配核对与测试用）。
func PortPolicyOf(s *Service) PortPolicy { return s.portPolicy() }
