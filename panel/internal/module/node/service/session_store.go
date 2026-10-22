package service

// 会话的落库（SessionBackend 的 Record* 三项）：节点自报的值先按列约束规整（截断、去 NUL、零值存 NULL），
// 再交 repo 按主键写。

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 与 00018 的列约束一致的上限。
const (
	maxAgentVersionChars = 64
	maxHostnameChars     = 255
	maxOSNameChars       = 64
	maxArchChars         = 32
	maxRuntimeReason     = 200
	maxFailReasonChars   = 64
	maxFailDetailBytes   = 2048
	maxCertIdentifier    = 253
	maxCertDetailBytes   = 1024
)

var (
	sha256Hex        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	certErrorCodePat = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// cleanText 是节点自报文本（Hello、心跳原因、配置回执原因码、接入时的主机信息）唯一的规整入口：
// 去非法 UTF-8、去掉全部 unicode.IsControl（含 NUL、换行、TAB、ESC、DEL、C1）、TrimSpace、按 rune 截到 maxChars、再 TrimSpace；空串返回 nil。
func cleanText(s string, maxChars int) *string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > maxChars {
		s = string([]rune(s)[:maxChars])
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return nil
	}
	return &s
}

// truncateBytes 按 UTF-8 字符边界截到至多 n 字节（先去掉 NUL 与非法 UTF-8）；空串返回 nil。
func truncateBytes(s string, n int) *string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, ""), "\x00", "")
	if len(s) > n {
		cut := n
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	if s == "" {
		return nil
	}
	return &s
}

// positiveInt32 把节点自报的数量转成列值：0 存 NULL（未知），超出 int32 封顶。
func positiveInt32(v uint64) *int32 {
	if v == 0 {
		return nil
	}
	if v > math.MaxInt32 {
		v = math.MaxInt32
	}
	n := int32(v) //nolint:gosec // 上面已封顶到 MaxInt32
	return &n
}

// agentInfo 把 Hello 规整成 servers 的列值。
func agentInfo(h Hello) repo.ServerAgentInfo {
	var sha *string
	if v := strings.ToLower(strings.TrimSpace(h.BinarySHA256)); sha256Hex.MatchString(v) {
		sha = &v
	}
	return repo.ServerAgentInfo{
		AgentVersion: cleanText(h.AgentVersion, maxAgentVersionChars),
		BinarySHA256: sha,
		Hostname:     cleanText(h.Host.Hostname, maxHostnameChars),
		OSName:       cleanText(h.Host.OS, maxOSNameChars),
		Architecture: cleanText(h.Host.Arch, maxArchChars),
		CPUCores:     positiveInt32(uint64(h.Host.CPUCores)),
		MemoryMB:     positiveInt32(h.Host.MemoryBytes >> 20),
		DiskGB:       positiveInt32(h.Host.DiskBytes >> 30),
	}
}

// RecordHello 记 Hello：先记 Hello 带来的上一次升级结局；自报信息变了才写（自报信息不推，后台靠轮询）；
// 最后心跳时间照写（建流即视为一拍心跳）；再按摘要对账在线升级任务。
//
// 节点拿到 Welcome 就删本机的结局文件，所以结局记不上时返回 ErrUpgradeOutcomeUnrecorded，建流随之失败、不发 Welcome，
// 节点留着文件重连再报；其余写失败只是展示数据，建流照常。
func (s *Service) RecordHello(ctx context.Context, serverID uuid.UUID, h Hello, at time.Time) error {
	if h.LastUpgrade != nil {
		if err := s.RecordUpgradeReport(ctx, serverID, *h.LastUpgrade, at); err != nil {
			return fmt.Errorf("%w: %w", ErrUpgradeOutcomeUnrecorded, err)
		}
	}
	if _, err := s.repo.UpdateServerAgent(ctx, serverID, agentInfo(h)); err != nil {
		return err
	}
	prev, found, err := s.repo.TouchServerHeartbeat(ctx, serverID, at)
	if err != nil {
		return err
	}
	if found && beatFlipsOnline(prev, at) {
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
	}
	return s.succeedUpgradeBySHA(ctx, serverID, h, at)
}

// certRowFrom 把心跳里的证书状态规整成列值；n.Cert 为 nil 时五列全 nil。
func certRowFrom(c *NodeCert) (id *string, notAfter, next *time.Time, code, detail *string) {
	if c == nil {
		return nil, nil, nil, nil, nil
	}
	id = cleanText(c.Identifier, maxCertIdentifier)
	notAfter = c.NotAfter
	next = c.NextAttempt
	if raw := cleanText(c.ErrorCode, 64); raw != nil && certErrorCodePat.MatchString(*raw) {
		code = raw
		// 详情是 CA 的原话，保留换行等排版，只去 NUL 与非法 UTF-8 并按字节截断（列上限 1 KiB）
		detail = truncateBytes(c.ErrorDetail, maxCertDetailBytes)
	}
	return id, notAfter, next, code, detail
}

// runtimeRows 把心跳里的运行状态规整成列值：认不出的状态丢掉，同一节点后出现的为准，running 不带原因。
func runtimeRows(nodes []NodeRuntime) []repo.NodeRuntimeRow {
	idx := make(map[uuid.UUID]int, len(nodes))
	rows := make([]repo.NodeRuntimeRow, 0, len(nodes))
	for _, n := range nodes {
		switch n.Status {
		case RuntimeRunning, RuntimeDegraded, RuntimeDown:
		default:
			continue
		}
		row := repo.NodeRuntimeRow{NodeID: n.NodeID, Status: n.Status}
		if n.Status != RuntimeRunning {
			row.Reason = cleanText(n.Reason, maxRuntimeReason)
			row.ReasonNodeID = n.ReasonNodeID
		}
		row.CertIdentifier, row.CertNotAfter, row.CertNextAttempt, row.CertErrorCode, row.CertErrorDetail = certRowFrom(n.Cert)
		if i, ok := idx[n.NodeID]; ok {
			rows[i] = row
			continue
		}
		idx[n.NodeID] = len(rows)
		rows = append(rows, row)
	}
	return rows
}

// RecordHeartbeat 记一拍心跳：服务器的最后心跳时间每拍写；节点运行状态只写这台服务器名下、真的变了的
// （不属于这台服务器的节点 id 由 SQL 条件忽略）。
func (s *Service) RecordHeartbeat(ctx context.Context, serverID uuid.UUID, nodes []NodeRuntime, at time.Time) error {
	prev, found, err := s.repo.TouchServerHeartbeat(ctx, serverID, at)
	if err != nil {
		return err
	}
	if found && beatFlipsOnline(prev, at) {
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
	}
	ids, err := s.repo.UpdateNodeRuntimes(ctx, serverID, runtimeRows(nodes), at)
	if err != nil {
		return err
	}
	s.nodesChanged(ctx, ids)
	return nil
}

// RecordConfigAck 记配置回执（会话层已核对摘要是最近下发的那份）：
//   - switched、health_passed：节点已在跑这一版 → 写 applied_config_*，清掉 apply_failed_*。
//     health_passed 不单独存：pdnd 只在内核有就绪探测时才报它，入站是否在服务看心跳的运行状态。
//   - failed：写 apply_failed_*（原因码缺省 unknown，详情截到 2KB）；已应用的版本不动（旧配置仍在服务）。
func (s *Service) RecordConfigAck(ctx context.Context, serverID uuid.UUID, a ConfigAck, at time.Time) error {
	sha := strings.ToLower(a.SHA256)
	if !sha256Hex.MatchString(sha) {
		return nil
	}
	switch a.Phase {
	case ConfigSwitched, ConfigHealthPassed:
		wrote, err := s.repo.MarkConfigApplied(ctx, serverID, a.NodeID, sha, at)
		if err != nil {
			return err
		}
		if wrote {
			s.nodeChanged(ctx, realtime.OpUpdate, a.NodeID)
		}
		return nil
	case ConfigFailed:
		reason := "unknown"
		if r := cleanText(a.ReasonCode, maxFailReasonChars); r != nil {
			reason = *r
		}
		wrote, err := s.repo.MarkConfigFailed(ctx, serverID, a.NodeID, sha, reason, truncateBytes(a.Detail, maxFailDetailBytes), at)
		if err != nil {
			return err
		}
		if wrote {
			s.nodeChanged(ctx, realtime.OpUpdate, a.NodeID)
		}
		return nil
	}
	return nil
}
