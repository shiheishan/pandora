package nodefabric

// 协议配置里「安全与可连通」层面的收紧规则（2026-10 w4proto）。
//
// 这些规则不关心字段形状（那是 protocol_validate.go 的严格解码），只回答
// 两个问题：存进去的配置节点能不能真起来、客户端能不能真连上；以及这份
// 配置会不会被探测者或配置者拿来打节点本机、内网。凡是答不上来的组合，
// 保存时就拦住，而不是等用户对着「超时」排查。

import (
	"encoding/json"
	"net"
	"net/netip"
	"path"
	"strconv"
	"strings"
)

// 内核选择：只剩 NativeCore。
//
// 面板曾允许把 kernel 设成 sing-box 或 xray-core，但生产的 pdnd 只链接
// NativeCore（pdnd/runtime_native.go），根本不读这个字段；只有带 -tags compat
// 单独构建的迁移版本才按它分派。新写入因此只接受 auto / pandora-native，
// 存量的 sing-box / xray-core 读时忽略（见 EffectiveKernel），不强制改写。
var acceptedKernels = []string{"auto", "pandora-native"}

// legacyKernels 是曾经开放、现在只读兼容的内核值。
var legacyKernels = []string{"sing-box", "xray-core"}

const kernelFieldMessage = "只能是 auto 或 pandora-native：节点端只有 NativeCore，sing-box / xray-core 已不再生效"

// validateKernelChoice 校验一次写入里的 kernel。空串等同 auto。
func validateKernelChoice(fields map[string]string, kernel string) {
	kernel = strings.TrimSpace(kernel)
	if kernel == "" || containsString(acceptedKernels, kernel) {
		return
	}
	fields["kernel"] = kernelFieldMessage
}

// EffectiveKernel 是下发与展示时实际生效的内核值。
//
// 存量节点里还留着 sing-box / xray-core：NativeCore 不读它，下发时一律按 auto
// 给出（也不再带 kernel_type），免得兼容构建按旧值把数据面切到别的内核，
// 或在 native-only 模式下拒绝起入站。库里的值不改写，管理员下次改协议时
// 表单会把它换成 auto。
func EffectiveKernel(kernel string) string {
	kernel = strings.TrimSpace(kernel)
	if kernel == "" || containsString(legacyKernels, kernel) {
		return "auto"
	}
	return kernel
}

// utlsFingerprints 是 uTLS 客户端指纹的可选值：mihomo 的 client-fingerprint
// 与 sing-box 的 utls.fingerprint 都认的那一组（xray 的 fp 是它们的超集）。
// randomized 只有 sing-box / xray 认，mihomo 不认，不放进来——订阅三种格式
// 用的是同一个值，一个格式不认就等于那个客户端连不上。
var utlsFingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random"}

// vlessFlows 是 VLESS 流控的可选值，与 pdnd kernel/vless_flow.go 认识的一致。
// xtls-rprx-direct / splice 是上游已删除的早期方案，节点端会当场拒绝。
var vlessFlows = []string{"xtls-rprx-vision", "xtls-rprx-vision-udp443"}

// validateFingerprint 校验 uTLS 指纹（内核字段名 fingerprint，表单叫 utls）。
func validateFingerprint(fields map[string]string, fingerprint string) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" || containsString(utlsFingerprints, fingerprint) {
		return
	}
	fields["protocol_config.fingerprint"] = "uTLS 指纹只能是 " + strings.Join(utlsFingerprints, "、")
}

// validateVLESSFlow 校验流控：取值是枚举，而且只有 REALITY + tcp 能用。
//
// Vision 要拿到 TLS 之下的原始 TCP 连接做拼接，xray / mihomo / sing-box 都只在
// 裸 TCP 传输上支持它；挂在 grpc、ws、xhttp 上时客户端直接报错，订阅里的
// 这个节点等于废了。不加密时没有 TLS 可「看穿」，流控没有意义。
func validateVLESSFlow(fields map[string]string, flow, network, security string) {
	flow = strings.TrimSpace(flow)
	if flow == "" {
		return
	}
	if !containsString(vlessFlows, flow) {
		fields["protocol_config.flow"] = "flow 只能是 " + strings.Join(vlessFlows, " 或 ") + "，或者留空"
		return
	}
	if !strings.EqualFold(strings.TrimSpace(security), "reality") || normalizedNetwork(network) != "tcp" {
		fields["protocol_config.flow"] = "Vision 流控只能用在 REALITY + tcp 上，其它组合请留空"
	}
}

// realityNetworks 是 REALITY 允许的传输。
//
// REALITY 是 TCP 上的 TLS 层：mkcp 走 UDP，内核却按 TCP 起 REALITY，客户端
// 永远连不上；ws / httpupgrade 不是任何主流客户端的标准组合（Xray 服务端也
// 不接受）。xhttp-h3 的 REALITY 是 QUIC 版，节点端标着实验、外部客户端未验证，
// 订阅三种格式也都跳过它，存进去就是一个谁都拿不到的节点。
var realityNetworks = []string{"tcp", "grpc", "xhttp"}

func validateRealityNetwork(fields map[string]string, network string) {
	if !containsString(realityNetworks, normalizedNetwork(network)) {
		fields["protocol_config.network"] = "REALITY 只能配 tcp、grpc 或 xhttp 传输"
	}
}

// plaintextStreamNetworks 是不加密时仍允许的传输：它们走 HTTP，可以挂在
// CDN 后面由 CDN 提供 TLS。裸 tcp 不加密就是明文代理——UUID 和目标地址
// 全在线路上裸奔，一眼就能被识别和封锁，所以默认拒绝。
func validatePlaintextStream(fields map[string]string, network string) {
	if normalizedNetwork(network) == "tcp" {
		fields["protocol_config.network"] = "tcp 不加密就是明文代理（UUID 和目标地址全裸）：请改用 REALITY，或换 ws / httpupgrade / grpc / xhttp 并套 CDN"
	}
}

func normalizedNetwork(network string) string {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		return "tcp"
	}
	return network
}

// numericTLSValue 读出 tls 键的数字值；不是数字（布尔、缺席、坏 JSON）时 ok=false。
func numericTLSValue(raw json.RawMessage) (int, bool) {
	var probe struct {
		TLS json.RawMessage `json:"tls"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return 0, false
	}
	var number json.Number
	if len(probe.TLS) == 0 || json.Unmarshal(probe.TLS, &number) != nil {
		return 0, false
	}
	value, err := strconv.Atoi(number.String())
	if err != nil {
		return -1, true
	}
	return value, true
}

// plainTLSNotYetMessage 是 vless / vmess 选普通 TLS（tls=1）时的报错。
const plainTLSNotYetMessage = "普通 TLS 需要证书，证书自动申请上线后开放；现在请选 REALITY（vless），或不加密并套 CDN"

// ---------------------------------------------------------------------------
// 地址：REALITY dest 与回落目标都只许指向公网
// ---------------------------------------------------------------------------

// reservedHostSuffixes 是字面上就指向本机或局域网的域名后缀
// （RFC 6761 localhost、RFC 6762 .local、RFC 8375 home.arpa、ICANN 保留的
// .internal，以及常见的 localdomain）。只做字面判断，保存时不做 DNS 解析。
var reservedHostSuffixes = []string{"localhost", "local", "localdomain", "internal", "home.arpa"}

// cgnatPrefix 是运营商级 NAT 的共享地址段（RFC 6598），和私网一样不该出现在
// 一个「转发到公网站点」的目标里。
var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// nonPublicHostReason 判断 host（不含端口）是否字面上指向本机、私网或链路本地；
// 是则返回原因，否则返回空串。
func nonPublicHostReason(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return "主机为空"
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		addr = addr.Unmap()
		switch {
		case addr.IsLoopback():
			return "不能是回环地址"
		case addr.IsUnspecified():
			return "不能是 0.0.0.0 或 ::"
		case addr.IsPrivate() || cgnatPrefix.Contains(addr):
			return "不能是私网地址"
		case addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast():
			return "不能是链路本地地址"
		case addr.IsMulticast() || addr.IsInterfaceLocalMulticast():
			return "不能是组播地址"
		}
		return ""
	}
	for _, suffix := range reservedHostSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return "不能指向 " + suffix + "（本机或局域网域名）"
		}
	}
	if !strings.Contains(host, ".") {
		// 单标签主机名靠本机的搜索域解析，指向的几乎总是内网服务
		return "要填完整域名，单个标签的主机名会解析到内网"
	}
	if !validServerName(host) {
		return "不是有效的域名"
	}
	return ""
}

// realityDestReason 在 validateRealityFields 的格式检查之后，拒绝字面上
// 指向本机或内网的 dest：认证失败的连接会被原样转发给 dest，填成
// localhost:6379 就等于把扫描器的流量送进本机 Redis。
func realityDestReason(host string) string {
	if reason := nonPublicHostReason(host); reason != "" {
		return "dest " + reason + "：认证失败的探测会被转发过去"
	}
	return ""
}

// validateProbeFallback 校验可选的回落目标（trojan / anytls / naive 的 fallback）。
//
// 形状与 pdnd kernel/probe_fallback.go:parseProbeFallback 一致：显式的
// host:port，不接受 URL、路径、用户信息。面板另外拒绝回环、私网、链路本地
// 和保留域名：回落是给认证失败的探测看的，探测者能决定往回落里写什么，
// 指向内网就是把节点变成打内网服务的跳板。
func validateProbeFallback(fields map[string]string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	const key = "protocol_config.fallback"
	if strings.Contains(value, "://") || strings.ContainsAny(value, "/?#@ \t\r\n") {
		fields[key] = "回落目标只填 host:port，不要带 http://、路径或参数"
		return
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" {
		fields[key] = "回落目标格式应为 host:port，例如 www.example.com:80"
		return
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		fields[key] = "回落目标的端口必须在 1–65535 之间"
		return
	}
	if reason := nonPublicHostReason(host); reason != "" {
		fields[key] = "回落目标" + reason + "：探测流量会被转发过去"
	}
}

// validateTrojanFallbackNetwork：Trojan 只在 TCP 直连承载上记录认证前的字节并
// 回落（pdnd trojan.go:serveConn 的 probe 参数），ws / grpc 等传输上 fallback
// 会被忽略。存一个不生效的回落只会让人以为抗探测已经开了。
func validateTrojanFallbackNetwork(fields map[string]string, fallback, network string) {
	if strings.TrimSpace(fallback) == "" {
		return
	}
	if normalizedNetwork(network) != "tcp" {
		if _, taken := fields["protocol_config.fallback"]; !taken {
			fields["protocol_config.fallback"] = "回落只在 tcp 传输上生效，其它传输请留空"
		}
	}
}

// ---------------------------------------------------------------------------
// 证书路径
// ---------------------------------------------------------------------------

// NodeCertificateDir 是节点证书文件的约定目录（docs/node-certificates.md 第 1 节）。
const NodeCertificateDir = "/etc/pandora-native/certs/"

// certificatePathProblem 返回证书 / 私钥路径的问题；合规返回空串。
//
// 与文档 1.2 节的字面规则一致：绝对路径，规范化后（本就该是规范形式）落在
// 约定目录下，不含 |。符号链接与文件大小只有节点端能查，不在这里做。
func certificatePathProblem(p string) string {
	if strings.TrimSpace(p) != p || strings.ContainsAny(p, "|\r\n\t\x00") {
		return "路径不能有首尾空白、换行或 |"
	}
	if !strings.HasPrefix(p, "/") {
		return "必须是绝对路径，放在 " + NodeCertificateDir + " 下"
	}
	if path.Clean(p) != p {
		return "路径不能含 .、.. 或重复的 /，请填规范的绝对路径"
	}
	if !strings.HasPrefix(p, NodeCertificateDir) || len(p) == len(NodeCertificateDir) {
		return "证书文件必须放在 " + NodeCertificateDir + " 下（见 docs/node-certificates.md）"
	}
	return ""
}

// validateCertificatePaths 对非空的 cert_path / key_path 做路径检查。
// 必填与否由各协议自己的分支判断，这里不重复；已经有报错的键不覆盖。
func validateCertificatePaths(fields map[string]string, raw json.RawMessage) {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg == nil {
		return
	}
	for _, key := range []string{"cert_path", "key_path"} {
		value, ok := cfg[key].(string)
		if !ok || value == "" {
			continue
		}
		if _, taken := fields["protocol_config."+key]; taken {
			continue
		}
		if problem := certificatePathProblem(value); problem != "" {
			fields["protocol_config."+key] = problem
		}
	}
}

// ProtocolConfigWarnings 给存量节点的读接口用：库里的配置不满足现行规则、
// 但不强制改写的地方，返回给后台看的提示。目前只有证书路径一类。
//
// 节点照常下发、照常服务；管理员下次修改协议时校验会要求改正。
func ProtocolConfigWarnings(nodeType string, raw json.RawMessage) []string {
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg == nil {
		return nil
	}
	kernel := toKernelConfig(CanonicalNodeType(nodeType), cfg)
	var out []string
	for _, key := range []string{"cert_path", "key_path"} {
		value, ok := kernel[key].(string)
		if !ok || value == "" {
			continue
		}
		if problem := certificatePathProblem(value); problem != "" {
			out = append(out, key+" 「"+value+"」"+problem+"；节点照常服务，下次修改协议时需要改正")
		}
	}
	return out
}
