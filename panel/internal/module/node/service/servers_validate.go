package service

// 服务器输入校验与读模型转换（纯函数，不碰 repo）。

import (
	"fmt"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const (
	serverNameMax          = 64
	serverRegionMax        = 64
	serverHostnameMax      = 253
	serverArchitectureMax  = 32
	serverOSNameMax        = 64
	serverNotesMax         = 2000
	serverReasonMax        = 200
	serverQueryMax         = 120
	serverDefaultCapacity  = 32
	serverNameMessage      = "名称必须为 1 到 64 个字符"
	serverNulMessage       = "不能包含空字符"
	serverIPMessage        = "IP 地址格式不正确"
	serverCapacityMessage  = "必须大于 0"
	serverRowVersionNeeded = "必须提供正整数版本号"
)

// serverIPKind 是 IP 字段要求的地址族。
type serverIPKind int

const (
	serverIPv4 serverIPKind = iota
	serverIPv6
)

// serverCheckText 校验一个可选文本字段：含 NUL 或超长时记进 fields。
func serverCheckText(fields map[string]string, key, v string, max int) {
	if strings.ContainsRune(v, 0) {
		fields[key] = serverNulMessage
		return
	}
	if utf8.RuneCountInString(v) > max {
		fields[key] = fmt.Sprintf("内容过长，最多允许 %d 个字符", max)
	}
}

// serverCheckName 校验名称（已 trim）。
func serverCheckName(fields map[string]string, v string) {
	if strings.ContainsRune(v, 0) {
		fields["name"] = serverNulMessage
		return
	}
	if n := utf8.RuneCountInString(v); n < 1 || n > serverNameMax {
		fields["name"] = serverNameMessage
	}
}

// serverCheckIP 校验一个可选 IP 字段（已 trim，空串表示不填）。
func serverCheckIP(fields map[string]string, key, v string, kind serverIPKind) {
	if v == "" {
		return
	}
	if strings.ContainsRune(v, 0) {
		fields[key] = serverNulMessage
		return
	}
	ip := net.ParseIP(v)
	ok := ip != nil
	if ok && kind == serverIPv4 {
		ok = ip.To4() != nil && !strings.Contains(v, ":")
	}
	if ok && kind == serverIPv6 {
		ok = ip.To4() == nil
	}
	if !ok {
		fields[key] = serverIPMessage
	}
}

// serverValidateInput 规整并校验新建服务器的输入；CapacityNodes 为 0 取缺省 32。错误一次全收。
func serverValidateInput(in *ServerInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Region = strings.TrimSpace(in.Region)
	in.Hostname = strings.TrimSpace(in.Hostname)
	in.PublicIPv4 = strings.TrimSpace(in.PublicIPv4)
	in.PublicIPv6 = strings.TrimSpace(in.PublicIPv6)
	in.PrivateIPv4 = strings.TrimSpace(in.PrivateIPv4)
	in.Architecture = strings.TrimSpace(in.Architecture)
	in.OSName = strings.TrimSpace(in.OSName)
	in.Notes = strings.TrimSpace(in.Notes)

	fields := map[string]string{}
	serverCheckName(fields, in.Name)
	serverCheckText(fields, "region", in.Region, serverRegionMax)
	serverCheckText(fields, "hostname", in.Hostname, serverHostnameMax)
	serverCheckText(fields, "architecture", in.Architecture, serverArchitectureMax)
	serverCheckText(fields, "os_name", in.OSName, serverOSNameMax)
	serverCheckText(fields, "notes", in.Notes, serverNotesMax)
	serverCheckIP(fields, "public_ipv4", in.PublicIPv4, serverIPv4)
	serverCheckIP(fields, "public_ipv6", in.PublicIPv6, serverIPv6)
	serverCheckIP(fields, "private_ipv4", in.PrivateIPv4, serverIPv4)
	if in.CapacityNodes == 0 {
		in.CapacityNodes = serverDefaultCapacity
	} else if in.CapacityNodes < 0 {
		fields["capacity_nodes"] = serverCapacityMessage
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// serverTrimPtr 就地 trim 一个可选文本。
func serverTrimPtr(p *string) {
	if p != nil {
		*p = strings.TrimSpace(*p)
	}
}

// serverValidatePatch 规整并校验改服务器的输入：只校验给了的字段。
func serverValidatePatch(in *ServerPatch) error {
	for _, p := range []*string{in.Name, in.Region, in.Hostname, in.PublicIPv4, in.PublicIPv6,
		in.PrivateIPv4, in.Architecture, in.OSName, in.Notes} {
		serverTrimPtr(p)
	}
	fields := map[string]string{}
	if in.RowVersion <= 0 {
		fields["row_version"] = serverRowVersionNeeded
	}
	if in.Name != nil {
		serverCheckName(fields, *in.Name)
	}
	texts := []struct {
		key string
		v   *string
		max int
	}{
		{"region", in.Region, serverRegionMax},
		{"hostname", in.Hostname, serverHostnameMax},
		{"architecture", in.Architecture, serverArchitectureMax},
		{"os_name", in.OSName, serverOSNameMax},
		{"notes", in.Notes, serverNotesMax},
	}
	for _, t := range texts {
		if t.v != nil {
			serverCheckText(fields, t.key, *t.v, t.max)
		}
	}
	if in.PublicIPv4 != nil {
		serverCheckIP(fields, "public_ipv4", *in.PublicIPv4, serverIPv4)
	}
	if in.PublicIPv6 != nil {
		serverCheckIP(fields, "public_ipv6", *in.PublicIPv6, serverIPv6)
	}
	if in.PrivateIPv4 != nil {
		serverCheckIP(fields, "private_ipv4", *in.PrivateIPv4, serverIPv4)
	}
	if in.CapacityNodes != nil && *in.CapacityNodes <= 0 {
		fields["capacity_nodes"] = serverCapacityMessage
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// serverValidateQuery 校验服务器列表的筛选（Query 就地 trim）。
func serverValidateQuery(f *ServerFilter) error {
	f.Status = strings.TrimSpace(f.Status)
	f.Query = strings.TrimSpace(f.Query)
	fields := map[string]string{}
	if f.Status != "" && !ValidServerStatus(f.Status) {
		fields["status"] = "不支持的服务器状态"
	}
	if strings.ContainsRune(f.Query, 0) {
		fields["q"] = serverNulMessage
	} else if utf8.RuneCountInString(f.Query) > serverQueryMax {
		fields["q"] = "搜索词不能超过 120 个字符"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// serverFromModel 把一行服务器与它的节点计数转成对外的 Server。
func serverFromModel(m model.Server, c repo.ServerNodeCount, now time.Time) Server {
	out := Server{
		ID:              m.ID,
		Name:            m.Name,
		Status:          m.Status,
		StatusReason:    m.StatusReason,
		RowVersion:      m.RowVersion,
		Region:          m.Region,
		Hostname:        m.Hostname,
		PublicIPv4:      m.PublicIPv4,
		PublicIPv6:      m.PublicIPv6,
		PrivateIPv4:     m.PrivateIPv4,
		Architecture:    m.Architecture,
		OSName:          m.OSName,
		AgentVersion:    m.AgentVersion,
		BinarySHA256:    m.BinarySHA256,
		LastHeartbeatAt: m.LastHeartbeatAt,
		CPUCores:        m.CPUCores,
		MemoryMB:        m.MemoryMB,
		DiskGB:          m.DiskGB,
		CapacityNodes:   m.CapacityNodes,
		Notes:           m.Notes,
		NodeCount:       c.Total,
		ActiveNodeCount: c.Active,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
	if m.LastHeartbeatAt != nil {
		out.HeartbeatOnline = m.Status == ServerReady && now.Sub(*m.LastHeartbeatAt) < NodeStaleAfter
		out.ServingNodeCount = c.Active
	} else {
		out.NeverSeenNodeCount = c.Active
	}
	return out
}

// serverOptText 把规整后的文本转成可空列：空串返回 nil。
func serverOptText(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}
