package nodefabric

// 管理端用 xboard 的字段名，内核继续用它原来认识的那套，中间隔一层翻译。
//
// 起因很直接：后台的协议参数得和 xboard 长得一样，运维照着别处的教程和
// 截图就能填。但 protocol_config 原来是**原样平铺**下发给节点的——
// BuildNodeConfig 把它整个摊到配置顶层，字段名就是和内核的通信契约。
// 跟着改名意味着 nodeagent 和 pdnd 两个内核都要同步改，还要给现网所有
// 节点的存量配置做数据迁移。改错了不会报错，是节点静默连不上，跟排查
// 心跳那次一模一样的坑。
//
// 所以换个做法：管理端存 xboard 形状，下发前翻译回内核字段。
//
//   后台表单 / API      →   protocol_config（xboard 形状）
//                              ↓ toKernelConfig
//                       下发给节点（内核原有字段，一字未改）
//
// 这样数据面根本不知道上层改过名。代价是多一张映射表，而这张表是纯数据、
// 有测试守着，比让两个内核跟着改安全得多。
//
// 映射只处理「同一个概念两边叫法不同」的字段。两边同名的（network、flow、
// congestion_control 之类）不进表，直接透传。

// xboardRename 是「xboard 名 → 内核名」的平坦重命名。
//
// 按协议分开而不是做一张全局表：cipher 在 shadowsocks 里是加密方式，
// 换个协议可能是别的东西。全局表在加协议时会悄悄把不相干的字段也改掉。
var xboardRename = map[string]map[string]string{
	"shadowsocks": {
		// xboard 叫 cipher，sing-box / xray 的入站配置里叫 method。
		"cipher": "method",
	},
	"hysteria2": {
		// xboard 的带宽是 bandwidth.up / bandwidth.down（嵌套），
		// 展平后是 bandwidth_up / bandwidth_down，内核要的是 up_mbps / down_mbps。
		"bandwidth_up":   "up_mbps",
		"bandwidth_down": "down_mbps",
		// obfs 不在这里：我们的内核本来就收 {type, password} 对象，
		// 和 xboard 的形状一模一样，不需要任何转换。一开始我按「xboard 是
		// 对象、内核是字符串」写了个 obfs_password → obfs 的映射，查了
		// 校验器才发现内核那边 Obfs 是 map[string]any、还要求 type 必须是
		// salamander——那条映射会把对象压成字符串，把好好的配置改坏。
	},
	"vless":   xboardStreamRename,
	"vmess":   xboardStreamRename,
	"trojan":  xboardStreamRename,
	"mieru":   {},
	"tuic":    {},
	"anytls":  {},
	"juicity": {},
	"socks":   {},
	"http":    {},
	"naive":   {},
	// shadowtls 不在 xboard 的协议表里，保持我们自己的字段名。
	"shadowtls": {},
}

// xboardStreamRename 是 vless / vmess / trojan 共用的传输层重命名。
//
// xboard 把传输参数收在 network_settings 对象里，REALITY 收在
// reality_settings，普通 TLS 收在 tls_settings。我们的内核字段是扁平的，
// 所以这里的键是「展平后的 xboard 路径」，用下划线连接。
var xboardStreamRename = map[string]string{
	"network_settings_path":         "path",
	"network_settings_host":         "host",
	"network_settings_headers_Host": "host",
	"network_settings_serviceName":  "grpc_service_name",
	"network_settings_mode":         "mode",

	"reality_settings_public_key":  "public_key",
	"reality_settings_private_key": "private_key",
	"reality_settings_short_id":    "short_ids",
	"reality_settings_server_name": "server_names",
	"reality_settings_dest":        "dest",

	"tls_settings_server_name":    "server_name",
	"tls_settings_allow_insecure": "allow_insecure",

	// xboard 管 uTLS 指纹叫 utls，我们内核叫 fingerprint。
	"utls": "fingerprint",
}

// xboardContainers 是 xboard 用来分组参数的那几个嵌套对象，也是**唯一**
// 允许被摊平的键。
//
// 这里必须是白名单而不是「见到对象就摊」。现网 naive 节点的配置里有
//
//	"masquerade": {"url": "...", "type": "proxy", "rewrite_host": true}
//
// 这是内核要的完整对象，无差别摊平会把它拆成 masquerade_url、
// masquerade_type，内核读不到 masquerade 就当没配——伪装直接失效，
// 而且不报错。凡是不在这张表里的对象一律原样保留。
var xboardContainers = map[string]bool{
	"network_settings": true,
	"tls_settings":     true,
	"reality_settings": true,
	"obfs_settings":    true,
	"bandwidth":        true,
	"encryption":       true,
	// network_settings.headers.Host 要再下一层。
	"headers": true,
	"header":  true,
	"request": true,
}

// flattenXboardConfig 把 xboard 的分组容器摊成下划线连接的平坦键。
//
// xboard 的表单是嵌套的（network_settings.headers.Host），而映射表和内核
// 配置都是平的。先统一形状，映射表就只需要处理改名，不用同时处理层级。
//
// 不摊数组：short_id 之类本来就是列表，摊开会丢掉「它是个列表」这件事。
// 不摊白名单以外的对象：见 xboardContainers 上面那段。
func flattenXboardConfig(prefix string, in map[string]any, out map[string]any) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "_" + k
		}
		if child, ok := v.(map[string]any); ok && xboardContainers[k] {
			flattenXboardConfig(key, child, out)
			continue
		}
		out[key] = v
	}
}

// toKernelConfig 把管理端存的 xboard 形状翻译成内核认识的扁平配置。
//
// 三步：摊平 → 改名 → 按协议做个别形状调整。
//
// 未在映射表里的字段原样保留。这一点是刻意的：我们比 xboard 多出来的
// 那些字段（mKCP 的 mtu/tti、XHTTP 的各种 sc_* 参数、REALITY 的完整
// 一套）在 xboard 里没有对应名字，硬给它们编一个只会让两边都对不上。
func toKernelConfig(nodeType string, cfg map[string]any) map[string]any {
	flat := map[string]any{}
	flattenXboardConfig("", cfg, flat)

	rename := xboardRename[nodeType]
	out := make(map[string]any, len(flat))
	for k, v := range flat {
		if to, ok := rename[k]; ok {
			// 已经有值就不覆盖：network_settings.host 和
			// network_settings.headers.Host 都映射到 host，
			// 两个都填时以先到的为准而不是随机覆盖。
			if _, taken := out[to]; !taken {
				out[to] = v
			}
			continue
		}
		out[k] = v
	}

	applyKernelShapeFixups(nodeType, out)
	return out
}

// applyKernelShapeFixups 处理改名之外的形状差异。
//
// 这些是「同一个意思，两边表达方式不同」而不是单纯换个名字，映射表表达
// 不了，只能逐条写。每一条都注明为什么。
func applyKernelShapeFixups(nodeType string, cfg map[string]any) {
	switch nodeType {
	case "vless", "vmess", "trojan":
		// xboard 的 tls 是三态整数：0 不加密、1 普通 TLS、2 REALITY。
		// 我们内核这边由 security 枚举表达，tls 是个独立的布尔。
		//
		// 关键约束：校验器目前**明确禁止** tls=true——
		//
		//	"证书生命周期完成前仅允许 tls=false，请改用 security=reality"
		//
		// 证书签发轮换那套还没做完，全站只允许走 REALITY。所以 2 映射出来
		// 的是 security=reality 而**不是** tls=true，映射反了会被自己的
		// 校验器拒掉。
		//
		// vless/vmess 映射完把 tls 键删掉，不留一个 tls:false。现网 4 个在役
		// vless 节点存的就是「只有 security、没有 tls」，补一个 false 会改变
		// 下发内容，也就没法再断言这次改名对数据面完全无影响。Trojan 的旧
		// 校验器要求显式布尔值，因此在上面的分支保留 tls=false/true。
		//
		// 对 vless/vmess，1 原样透传不做映射：现网只有 5 个已退役节点是这个
		// 值，永远不会被下发；而新写入会在校验那层被挡住并提示改用 REALITY。
		// 在这里悄悄把它改成别的值，等于替用户做了一个他没要求、也看不见的
		// 决定。Trojan 则把 1 明确翻成普通 TLS 的布尔 true（见上方分支）。
		if raw, ok := cfg["tls"]; ok && isNumeric(raw) {
			switch asInt(raw) {
			case 1:
				if nodeType == "trojan" {
					if _, has := cfg["security"]; !has {
						cfg["security"] = "none"
					}
					cfg["tls"] = true
				}
			case 2:
				if _, has := cfg["security"]; !has {
					cfg["security"] = "reality"
				}
				// Trojan 的旧校验器需要显式 tls=false 才能表达
				// security=reality；vless/vmess 则把这个键完全省略，保持
				// 存量下发字节不变。
				if nodeType == "trojan" {
					cfg["tls"] = false
				} else {
					delete(cfg, "tls")
				}
			case 0:
				if _, has := cfg["security"]; !has {
					cfg["security"] = "none"
				}
				if nodeType == "trojan" {
					cfg["tls"] = false
				} else {
					delete(cfg, "tls")
				}
			}
		}
		// short_id / server_name 在 xboard 是单个字符串，内核要数组。
		// 已经是数组的原样放回，避免把存量数据重新装箱后类型变样。
		if v, ok := cfg["short_ids"]; ok {
			if _, already := v.([]any); !already {
				cfg["short_ids"] = asStringSlice(v)
			}
		}
		if v, ok := cfg["server_names"]; ok {
			if _, already := v.([]any); !already {
				cfg["server_names"] = asStringSlice(v)
			}
		}
	case "mieru":
		// xboard 的 transport 是大写 TCP/UDP，内核要小写。
		if v, ok := cfg["transport"].(string); ok {
			cfg["transport"] = lowerASCII(v)
		}
	}
}

// isNumeric 区分「xboard 的三态整数」和「内核的布尔」。
// JSON 反序列化出来的数字一律是 float64，但接口层也可能直接构造 int。
func isNumeric(v any) bool {
	switch v.(type) {
	case float64, float32, int, int64, int32:
		return true
	}
	return false
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case bool:
		if n {
			return 1
		}
		return 0
	}
	return 0
}

// asStringSlice 把「一个字符串」或「已经是数组」统一成字符串数组。
func asStringSlice(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return []string{}
		}
		return []string{x}
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{}
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
