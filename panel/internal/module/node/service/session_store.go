package service

// 会话的落库（SessionBackend 的 Record* 三项）：节点自报的值先按列约束规整（截断、去 NUL、零值存 NULL），
// 再交 repo 按主键写。

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/repo"
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
)

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// cleanText 去掉 NUL 与非法 UTF-8、首尾空白，按字符数截断；空串返回 nil（存 NULL）。
func cleanText(s string, maxChars int) *string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if utf8.RuneCountInString(s) > maxChars {
		s = string([]rune(s)[:maxChars])
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
	n := int32(v)
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

// RecordHello 记 Hello：自报信息变了才写；最后心跳时间照写（建流即视为一拍心跳）。
func (s *Service) RecordHello(ctx context.Context, serverID uuid.UUID, h Hello, at time.Time) error {
	if _, err := s.repo.UpdateServerAgent(ctx, serverID, agentInfo(h)); err != nil {
		return err
	}
	return s.repo.TouchServerHeartbeat(ctx, serverID, at)
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
	if err := s.repo.TouchServerHeartbeat(ctx, serverID, at); err != nil {
		return err
	}
	_, err := s.repo.UpdateNodeRuntimes(ctx, serverID, runtimeRows(nodes), at)
	return err
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
		_, err := s.repo.MarkConfigApplied(ctx, serverID, a.NodeID, sha, at)
		return err
	case ConfigFailed:
		reason := "unknown"
		if r := cleanText(a.ReasonCode, maxFailReasonChars); r != nil {
			reason = *r
		}
		_, err := s.repo.MarkConfigFailed(ctx, serverID, a.NodeID, sha, reason, truncateBytes(a.Detail, maxFailDetailBytes), at)
		return err
	}
	return nil
}
