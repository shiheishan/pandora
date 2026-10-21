package render

// 一次订阅渲染的完整输入（Request）与各格式共用的前处理。
//
// 前处理按顺序做四件事，都只改副本，不改传入的切片与 Config（节点可能来自进程内缓存）：
//  1. 按协议（types 参数）与节点名关键字（filter 参数）过滤，口径照 Xboard 的 ClientController；
//  2. 后台开了「节点名加协议前缀」时，名字前加 [vless]、[Hy2] 这类前缀；
//  3. 有信息行（后台开了「订阅里显示剩余流量与到期」）且过滤后还有节点时，在最前插入信息节点：
//     地址是本机 1 号端口（与过期提示同一个占位，连上去立即被拒，不会把流量送到任何地方），
//     协议用 Shadowsocks（每种格式都认）；它们只进手动选择的组，不进自动选择类的组；
//  4. 去重名（uniqueNodeNames）；Loon 与 Surfboard 先把名字里会破坏一行写法的字符换掉。
//
// 各格式拿到的是 renderCtx：信息节点（info 为真）排在最前，其后是真实节点。某种格式一个真实节点
// 都写不出时，信息节点也不写（只剩信息节点的订阅没有意义），返回的节点数只算真实节点。

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Status 是订阅的用量与到期，用于 Shadowrocket 的 STATUS 行、SIP008 的 bytes_* 与信息节点。
type Status struct {
	// Upload / Download / Total 是字节；Total 为 0 表示不限量
	Upload, Download, Total int64
	// Expire 是到期时刻；零值表示长期有效
	Expire time.Time
	// Loc 是显示日期用的时区（站点时区）；nil 按 UTC
	Loc *time.Location
}

// Request 是一次渲染的全部输入。
type Request struct {
	Format    Format
	Nodes     []Node
	ProxyUUID string
	UA        string
	// Options 是进程配置里的渲染可配项
	Options Options
	// Types 是 ParseTypes 的结果（规范化后的协议名）；空不按协议过滤
	Types []string
	// Keywords 是 ParseKeywords 的结果（已转小写）；空不按名字过滤
	Keywords []string
	// ProtocolPrefix 为真时节点名前加协议前缀（后台开关，缺省关）
	ProtocolPrefix bool
	// InfoLines 是信息节点的名字（InfoLines 的结果），按顺序插在最前；空不插
	InfoLines []string
	Status    Status
	// SubscribeURL 是这份订阅的链接，Surfboard 的 #!MANAGED-CONFIG 用；空串时去掉含它的行
	SubscribeURL string
}

// renderCtx 是前处理之后交给各格式的输入。
type renderCtx struct {
	// nodes 已过滤、已加前缀、已去重名；信息节点（info 为真）排在最前
	nodes        []Node
	uuid         string
	ua           string
	opt          Options
	status       Status
	subscribeURL string
}

// clashKind 是 renderClash 要出的 Clash 系变体。
type clashKind int

const (
	clashMeta clashKind = iota
	clashPremium
	clashStash
)

// 内置模板里的占位符，由 fillTemplate 替换。
const (
	phProxies      = "{{proxies}}"
	phProxyGroups  = "{{proxy_groups}}"
	phSubscribeURL = "{{subscribe_url}}"
)

// 信息节点的占位地址与加密方式（与过期提示同一个本机 1 号端口，expired.go）。
const infoMethod = "aes-128-gcm"

// maxKeywordRunes 是 filter 参数的长度上限，超过就不按名字过滤（照 Xboard）。
const maxKeywordRunes = 20

// maxTypesRunes 是 types 参数的长度上限，超过就不按协议过滤（Xboard 没有上限；全部协议名连写也远不到 200 字）。
const maxTypesRunes = 200

// typeAliases 把 types 参数里的写法映射到规范化的协议名（Node.Type）。
var typeAliases = map[string]string{
	"shadowsocks": "shadowsocks", "ss": "shadowsocks",
	"vmess": "vmess", "vless": "vless", "trojan": "trojan",
	"hysteria2": "hysteria2", "hysteria": "hysteria2", "hy2": "hysteria2",
	"tuic": "tuic", "anytls": "anytls", "naive": "naive", "juicity": "juicity",
	"socks": "socks", "socks5": "socks", "http": "http", "mieru": "mieru",
	"shadowtls": "shadowtls", "shadow-tls": "shadowtls",
}

// protocolPrefixes 是节点名的协议前缀（照 Xboard 的 PROTOCOL_PREFIXES，补上 Xboard 没有的几种协议）。
var protocolPrefixes = map[string]string{
	"shadowsocks": "[ss]", "vmess": "[vmess]", "vless": "[vless]", "trojan": "[trojan]",
	"hysteria2": "[Hy2]", "tuic": "[tuic]", "anytls": "[anytls]", "socks": "[socks]",
	"http": "[http]", "naive": "[naive]", "juicity": "[juicity]", "mieru": "[mieru]",
	"shadowtls": "[shadowtls]",
}

// splitParam 按 | , ｜ ，切开参数值，去掉空白与空项。
func splitParam(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '|' || r == ',' || r == '｜' || r == '，'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseTypes 解析 types 参数（如 "vless|ss,hysteria"）成规范化的协议名，去重、保持出现顺序。
// 空、"all" 或一个都认不出时返回 nil（不过滤，与 Xboard 一致）；超过 maxTypesRunes 也返回 nil。
func ParseTypes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "all") || utf8.RuneCountInString(raw) > maxTypesRunes {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range splitParam(raw) {
		t, ok := typeAliases[strings.ToLower(p)]
		if !ok || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// ParseKeywords 解析 filter 参数成小写关键字。空或超过 20 个字时返回 nil（不过滤，与 Xboard 一致）。
func ParseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || utf8.RuneCountInString(raw) > maxKeywordRunes {
		return nil
	}
	parts := splitParam(raw)
	if len(parts) == 0 {
		return nil
	}
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return parts
}

// InfoLines 是信息节点的名字：剩余流量、距下次重置（nextReset 晚于 now 且早于到期时才有）、到期日。
// 文案照 Xboard（剩余流量：…、距离下次重置剩余：N 天、套餐到期：…）。
func InfoLines(st Status, nextReset *time.Time, now time.Time) []string {
	remaining := "不限"
	if st.Total > 0 {
		left := st.Total - st.Upload - st.Download
		if left < 0 {
			left = 0
		}
		remaining = FormatBytes(left)
	}
	lines := []string{"剩余流量：" + remaining}
	if nextReset != nil && nextReset.After(now) && (st.Expire.IsZero() || nextReset.Before(st.Expire)) {
		days := int(math.Ceil(nextReset.Sub(now).Hours() / 24))
		lines = append(lines, fmt.Sprintf("距离下次重置剩余：%d 天", days))
	}
	lines = append(lines, "套餐到期："+expireDate(st, "长期有效"))
	return lines
}

// expireDate 是到期日（站点时区，到日）；长期有效时返回 none。
func expireDate(st Status, none string) string {
	if st.Expire.IsZero() {
		return none
	}
	loc := st.Loc
	if loc == nil {
		loc = time.UTC
	}
	return st.Expire.In(loc).Format("2006-01-02")
}

// FormatBytes 按 1024 进位写成两位小数（如 12.34 GB）。
func FormatBytes(b int64) string {
	const k = 1024.0
	v := float64(b)
	switch {
	case v >= k*k*k*k:
		return fmt.Sprintf("%.2f TB", v/(k*k*k*k))
	case v >= k*k*k:
		return fmt.Sprintf("%.2f GB", v/(k*k*k))
	case v >= k*k:
		return fmt.Sprintf("%.2f MB", v/(k*k))
	default:
		return fmt.Sprintf("%.2f KB", v/k)
	}
}

// prepare 做文件头说的前处理。
func prepare(r Request) renderCtx {
	nodes := filterNodes(r.Nodes, r.Types, r.Keywords)
	if r.ProtocolPrefix {
		for i := range nodes {
			nodes[i].Name = protocolPrefixes[nodes[i].Type] + strings.TrimSpace(nodes[i].Name)
		}
	}
	if len(nodes) > 0 && len(r.InfoLines) > 0 {
		withInfo := make([]Node, 0, len(r.InfoLines)+len(nodes))
		for _, line := range r.InfoLines {
			withInfo = append(withInfo, infoNode(line))
		}
		nodes = append(withInfo, nodes...)
	}
	if r.Format == FormatLoon || r.Format == FormatSurfboard {
		for i := range nodes {
			nodes[i].Name = lineSafeName(nodes[i].Name)
		}
	}
	return renderCtx{
		nodes:        uniqueNodeNames(nodes),
		uuid:         r.ProxyUUID,
		ua:           r.UA,
		opt:          r.Options,
		status:       r.Status,
		subscribeURL: r.SubscribeURL,
	}
}

// filterNodes 返回过滤后的副本（总是新切片，后面的步骤可以放心改名字）。
func filterNodes(nodes []Node, types, keywords []string) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if len(types) > 0 && !containsStr(types, n.Type) {
			continue
		}
		if len(keywords) > 0 && !nameMatches(n.Name, keywords) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func nameMatches(name string, keywords []string) bool {
	l := strings.ToLower(name)
	for _, k := range keywords {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// infoNode 是一条信息节点：名字就是信息，地址是本机 1 号端口。
func infoNode(name string) Node {
	return Node{
		Name: name, Type: "shadowsocks",
		Host: expiredPlaceholderHost, Port: expiredPlaceholderPort,
		Config: map[string]any{"method": infoMethod},
		info:   true,
	}
}

// lineSafeName 把 Loon / Surfboard 一行写法里有含义的字符换掉：= 与 , 分隔字段，" 是引号，
// 行首的 [ 会被当成段名，换行会断行；行首的 #、;、// 会让整行变成注释。
func lineSafeName(name string) string {
	r := strings.NewReplacer("=", " ", ",", " ", "\"", "'", "[", "【", "]", "】", "\r", " ", "\n", " ", "\t", " ")
	s := strings.Join(strings.Fields(r.Replace(name)), " ")
	for {
		s = strings.TrimLeft(s, " \t")
		if strings.HasPrefix(s, "//") {
			s = s[2:]
			continue
		}
		if strings.HasPrefix(s, "#") {
			s = s[1:]
			continue
		}
		if strings.HasPrefix(s, ";") {
			s = s[1:]
			continue
		}
		break
	}
	return s
}

// splitInfo 把信息节点与真实节点分开（各自保持顺序）。prepare 把信息节点放在最前，
// 所以只切 rc.nodes、不复制；info 截断了容量，往里 append 不会覆盖真实节点。
func (rc renderCtx) splitInfo() (info, real []Node) {
	i := 0
	for i < len(rc.nodes) && rc.nodes[i].info {
		i++
	}
	return rc.nodes[:i:i], rc.nodes[i:]
}

// fillTemplate 把模板里的占位换成 vals 里的值（原样替换，占位整行出现与行内出现一视同仁）。
// 从左往右扫，同一位置有多个占位能匹配时取最长的（等长按字典序），保证结果确定。
// 不用 strings.NewReplacer：每次拉取都要现建一个替换器，再经它的缓冲多拷一遍整份订阅。
func fillTemplate(tmpl string, vals map[string]string) []byte {
	keys := make([]string, 0, len(vals))
	size := len(tmpl)
	for k, v := range vals {
		keys = append(keys, k)
		size += len(v)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	var firsts strings.Builder
	for _, k := range keys {
		if k != "" && !strings.Contains(firsts.String(), k[:1]) {
			firsts.WriteString(k[:1])
		}
	}
	out := make([]byte, 0, size)
	for i := 0; i < len(tmpl); {
		// 先整段跳到下一个可能是占位开头的字节
		j := strings.IndexAny(tmpl[i:], firsts.String())
		if j < 0 {
			out = append(out, tmpl[i:]...)
			break
		}
		out = append(out, tmpl[i:i+j]...)
		i += j
		matched := false
		for _, k := range keys {
			if k != "" && strings.HasPrefix(tmpl[i:], k) {
				out = append(out, vals[k]...)
				i += len(k)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, tmpl[i])
			i++
		}
	}
	return out
}

// dropLinesWith 去掉模板里含 token 的整行。
func dropLinesWith(tmpl, token string) string {
	if !strings.Contains(tmpl, token) {
		return tmpl
	}
	lines := strings.SplitAfter(tmpl, "\n")
	var b strings.Builder
	for _, line := range lines {
		if !strings.Contains(line, token) {
			b.WriteString(line)
		}
	}
	return b.String()
}
