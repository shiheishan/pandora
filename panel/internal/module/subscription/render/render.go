// Package render 把一组可下发的线路渲染成客户端订阅（Clash Meta、Clash Premium、sing-box、URI）。
//
// 纯函数包：不碰数据库、不读配置、不依赖任何模块。输入的协议配置已是内核字段名
// （由 node 模块按下发给节点的同一张映射表翻译好），这里只管各格式的写法。
//
// 同一批节点要变成三种完全不同的东西：Clash 的 YAML、sing-box 的 JSON、
// 以及一串 base64 编码的 URI。字段名不是猜的 —— Clash 那套按 mihomo
// adapter/outbound 里的 `proxy:` tag 核对过，sing-box 按 option 包核对过。
// 写错一个字段的后果是用户导入失败，而错误信息出现在他的客户端里，我们看不到。
//
// # 连不上的组合一律跳过
//
// 不是每种协议在每种格式里都有对应物（Clash 没有 naive，URI 没有 shadowtls，
// sing-box 没有 xhttp）。遇到这种情况跳过该节点而不是编一个近似的写法：
// 一条导入后连不上的线路比少一条线路更糟，用户会以为是节点坏了。跳过的原因
// 由各格式的 nodeTo* 返回，测试逐格核对（matrix_test.go）。
// 更要紧的是绝不让一个节点拖垮整份订阅：一个客户端不认识的字段可能让整份
// 配置导入失败，所有节点一起消失。
package render

import (
	"fmt"
	"strings"
)

// Format 是输出格式。
type Format string

// 输出格式。
const (
	FormatClash Format = "clash"
	// FormatClashPremium 是不认 Meta 专有协议的 Clash 内核（ClashX、Clash for
	// Windows、Clash for Android 这一系）。内容类型与 FormatClash 相同，只是
	// 节点只给它认识的那几种；给它一条 vless 或 hysteria2，整份 YAML 加载失败。
	FormatClashPremium Format = "clash-premium"
	FormatSingbox      Format = "singbox"
	FormatURI          Format = "uri"
)

// Node 是一条可下发的线路。Type 是规范化后的协议名，Config 是内核字段名的协议配置（只读，渲染不改它）。
type Node struct {
	Name        string
	Type        string
	Host        string
	Port        int
	Config      map[string]any
	TrafficRate float64
}

// SingboxTemplate 是 sing-box 订阅附带模板的可配项，空字段用缺省值。
type SingboxTemplate struct {
	// GeositeURLPrefix / GeoIPURLPrefix 是规则集地址前缀（含结尾的 /），换成镜像时改它。
	GeositeURLPrefix string
	GeoIPURLPrefix   string
	// BlockAds 为真时按 geosite-category-ads-all 拦截广告域名（DNS 与路由两处都拦）。
	BlockAds bool
}

// Options 是渲染的可配项（来自进程配置，启动时定一次）。
type Options struct {
	Singbox SingboxTemplate
}

// DetectFormat 按 User-Agent 判断客户端想要什么。
//
// 这是「一个链接到处能用」的实现：用户拿到的订阅地址只有一个，
// 什么客户端来就给什么格式，不需要他自己去挑。显式参数只是给
// 特殊情况留的后门（比如在浏览器里想看某个具体格式）。
//
// # 认不出来的一律给 URI
//
// 判断错的代价不对称：给 Clash 用户一份 base64，它还能当订阅链接
// 直接导入；给一个只吃 base64 的客户端一份 YAML，它直接报错。
// 所以只有明确认识的 UA 才映射到 Clash / sing-box，其余全部回落到
// URI —— 那是兼容面最宽的一种。
//
// # 顺序有讲究
//
// Stash、FlClash 这些 UA 里都带 clash 字样，Hiddify 有些版本会同时
// 带 clash 和 sing-box。更具体的匹配必须排在前面，否则会被通配的
// clash 分支先截走。
func DetectFormat(ua, explicit string) Format {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "clash":
		// Xboard / V2board 迁过来的链接带的是 flag=clash，而 Premium 内核的
		// 客户端也在用它。按 UA 再分一次：认得出是 Premium 就给 Premium。
		if clashFlavor(strings.ToLower(ua)) == FormatClashPremium {
			return FormatClashPremium
		}
		return FormatClash
	case "meta", "clashmeta", "clash-meta", "mihomo", "verge", "stash":
		return FormatClash
	case "clash-premium", "premium":
		return FormatClashPremium
	case "singbox", "sing-box", "singbox-json", "hiddify", "karing":
		return FormatSingbox
	case "uri", "base64", "v2ray", "v2rayn", "v2rayng", "shadowrocket", "general":
		return FormatURI
	}

	l := strings.ToLower(ua)
	switch {
	// sing-box 内核系。SFI/SFA/SFM/SFT 分别是它的 iOS/Android/macOS/tvOS
	// 官方客户端，UA 里只有这四个缩写，认不出就只能回落。
	case strings.Contains(l, "sing-box"), strings.Contains(l, "sfi/"),
		strings.Contains(l, "sfa/"), strings.Contains(l, "sfm/"),
		strings.Contains(l, "sft/"), strings.Contains(l, "hiddify"),
		strings.Contains(l, "karing"):
		return FormatSingbox

	// 只认 URI/base64 的一批。放在 clash 分支之前：其中几个的 UA
	// 里也带 clash 字样，被通配截走就会拿到一份用不了的 YAML。
	case strings.Contains(l, "shadowrocket"), strings.Contains(l, "quantumult"),
		strings.Contains(l, "surge"), strings.Contains(l, "loon"),
		strings.Contains(l, "v2rayn"), strings.Contains(l, "v2rayng"),
		strings.Contains(l, "v2rayu"), strings.Contains(l, "qv2ray"),
		strings.Contains(l, "nekobox"), strings.Contains(l, "nekoray"),
		strings.Contains(l, "matsuri"), strings.Contains(l, "sagernet"),
		strings.Contains(l, "streisand"), strings.Contains(l, "potatso"),
		strings.Contains(l, "oneclick"), strings.Contains(l, "shadowsocks"):
		return FormatURI

	// Clash 内核系。
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"),
		strings.Contains(l, "stash"), strings.Contains(l, "meta"),
		strings.Contains(l, "flclash"), strings.Contains(l, "nyanpasu"):
		return clashFlavor(l)

	default:
		return FormatURI
	}
}

// clashFlavor 区分 Clash Meta（mihomo）系与 Premium 系，判定参考 Xboard：
// UA 里有 Meta 系标记的给 Meta，只剩 clash 字样的给 Premium。
//
// 判错的代价同样不对称：把 Meta 客户端当成 Premium，少几条 Meta 专有协议的
// 节点，配置照样能用；把 Premium 当成 Meta，整份 YAML 加载失败。所以拿不准的
// 「clash」一律按 Premium。Stash 维持原状给 Meta 形状（它支持 vless、
// hysteria2、tuic；anytls、mieru 是否支持未核实，见报告遗留）。
func clashFlavor(lowerUA string) Format {
	for _, marker := range []string{
		"meta", "mihomo", "verge", "flclash", "nyanpasu", "stash", "openclash",
		"koala", "clashmi",
	} {
		if strings.Contains(lowerUA, marker) {
			return FormatClash
		}
	}
	if strings.Contains(lowerUA, "clash") {
		return FormatClashPremium
	}
	return FormatClash
}

// UAFamily 返回审计与日志用的客户端家族，不含可识别信息。
func UAFamily(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "sing-box"), strings.Contains(l, "sfi/"), strings.Contains(l, "sfa/"):
		return "sing-box"
	case strings.Contains(l, "shadowrocket"):
		return "shadowrocket"
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"), strings.Contains(l, "stash"):
		return "clash"
	case strings.Contains(l, "quantumult"):
		return "quantumult"
	case strings.Contains(l, "surge"):
		return "surge"
	case strings.Contains(l, "v2ray"):
		return "v2ray"
	case l == "":
		return "none"
	default:
		return "other"
	}
}

// Render 把节点渲染成指定格式，返回内容、Content-Type 与实际写进去的节点数（连不上的组合会跳过）。
// proxyUUID 是这份订阅在节点上的身份；ua 用来挑 sing-box 的版本写法（singbox_template.go），
// 为空即认不出版本，给兼容写法。
//
// 传入的 nodes 可能来自进程内缓存、被所有命中者共享：这里只读不改（shared_test.go 守着）。
func Render(f Format, nodes []Node, proxyUUID, ua string, opt Options) ([]byte, string, int) {
	nodes = uniqueNodeNames(nodes)
	switch f {
	case FormatClash:
		return renderClash(nodes, proxyUUID, false)
	case FormatClashPremium:
		return renderClash(nodes, proxyUUID, true)
	case FormatSingbox:
		return renderSingbox(nodes, proxyUUID, singboxDialectFor(ua), opt.Singbox)
	default:
		return renderURI(nodes, proxyUUID)
	}
}

// uniqueNodeNames 让 Clash 的代理名与 sing-box 的出站 tag 不重名、不撞保留名。
// 传入顺序是确定的，所以加的后缀每次都一样。
func uniqueNodeNames(nodes []Node) []Node {
	out := append([]Node(nil), nodes...)
	baseNames := make([]string, len(out))
	counts := make(map[string]int, len(out))
	for i := range out {
		base := strings.TrimSpace(out[i].Name)
		if base == "" {
			base = "节点"
		}
		baseNames[i] = base
		counts[base]++
	}
	reserved := map[string]bool{
		"direct": true, "block": true, "自动选择": true, "节点选择": true,
		// Clash 的内置策略名：节点叫这个会和组里的 DIRECT / REJECT 撞名
		"DIRECT": true, "REJECT": true,
	}
	used := make(map[string]bool, len(out)+len(reserved))
	for name := range reserved {
		used[name] = true
	}
	// 先占住本来就唯一、又不是保留名的名字，再分配后缀，
	// 这样「香港, 香港, 香港 · 1」不会撞名。
	for _, name := range baseNames {
		if counts[name] == 1 && !reserved[name] {
			used[name] = true
		}
	}
	next := make(map[string]int, len(counts))
	for i := range out {
		base := baseNames[i]
		if counts[base] == 1 && !reserved[base] {
			out[i].Name = base
			continue
		}
		index := next[base]
		if index < 1 {
			index = 1
		}
		candidate := ""
		for {
			candidate = fmt.Sprintf("%s · %d", base, index)
			index++
			if !used[candidate] {
				break
			}
		}
		next[base] = index
		used[candidate] = true
		out[i].Name = candidate
	}
	return out
}
