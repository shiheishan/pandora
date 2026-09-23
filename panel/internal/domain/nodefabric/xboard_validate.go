package nodefabric

import (
	"encoding/json"
)

// 校验也走翻译，不重写校验器。
//
// ValidateProtocolConfig 是四百多行逐协议的严格解码：REALITY 的必填项、
// XHTTP 各参数的取值范围、ShadowTLS 的握手服务器、Hysteria2 的超时格式，
// 还有拒绝未知字段和重复键。这些是经过验证的资产，为了换一批字段名把它
// 重写一遍，风险远大于收益——校验写松了不会报错，只会让一份错配置顺利
// 保存下去，等到用户连不上才发现。
//
// 所以和下发那边同一个做法：管理端收 xboard 形状，翻译成内核形状再交给
// 原来的校验器，最后把错误里的字段名映射回 xboard 名，否则表单标不到红。
//
//	管理端输入（xboard 名）
//	   ↓ toKernelConfig
//	内核形状  →  ValidateProtocolConfig（一字未改）
//	   ↓ 错误字段名反向映射
//	返回给表单（xboard 名）

// ValidateAdminProtocolConfig 校验管理端提交的 xboard 形状协议配置。
func ValidateAdminProtocolConfig(nodeType, kernel string, port int,
	raw json.RawMessage) (int, map[string]string) {

	canonical := CanonicalNodeType(nodeType)

	// 大小、重复键、是不是 JSON 对象，这三项必须在**原始输入**上查。
	//
	// 翻译过程会重新序列化，重复键在那一步就被静默丢掉一个了；等到翻译
	// 之后再查，永远查不出来。而重复键恰恰是最需要拦的——
	// {"cipher":"aes-128-gcm","cipher":"none"} 这种输入，两个解析器可能
	// 取到不同的那一个。
	if len(raw) == 0 || len(raw) > 16*1024 {
		return 0, map[string]string{"protocol_config": "协议配置必须是 16 KiB 以内的 JSON 对象"}
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return 0, map[string]string{"protocol_config": "协议配置不能包含重复字段"}
	}

	var stored map[string]any
	if err := json.Unmarshal(raw, &stored); err != nil || stored == nil {
		return 0, map[string]string{"protocol_config": "协议配置必须是 JSON 对象"}
	}

	kernelRaw, err := json.Marshal(toKernelConfig(canonical, stored))
	if err != nil {
		return 0, map[string]string{"protocol_config": "协议配置无法序列化"}
	}

	version, fields := ValidateProtocolConfig(nodeType, kernel, port, kernelRaw)
	return version, renameErrorFieldsToXboard(canonical, fields)
}

// kernelToXboardField 是 xboardRename 的逆表，按协议缓存。
//
// 逆表在包初始化时从正表推出来，不手写第二份：两份表迟早会漂移，而漂移
// 的表现是「表单某个字段永远标不到红」——用户看到保存失败却不知道哪里错。
var kernelToXboardField = func() map[string]map[string]string {
	out := make(map[string]map[string]string, len(xboardRename))
	for nodeType, table := range xboardRename {
		inverse := make(map[string]string, len(table))
		for xboardName, kernelName := range table {
			// 多个 xboard 字段映射到同一个内核字段时（network_settings.host
			// 和 network_settings.headers.Host 都指向 host），逆向只能挑一个。
			// 取字典序最小的那个，至少保证结果稳定、不随 map 遍历顺序变。
			if prev, taken := inverse[kernelName]; !taken || xboardName < prev {
				inverse[kernelName] = xboardName
			}
		}
		out[nodeType] = inverse
	}
	return out
}()

// renameErrorFieldsToXboard 把校验错误里的内核字段名换回管理端看到的名字。
//
// 内核名带下划线的是摊平后的路径（network_settings_path），要还原成点号
// 形式（network_settings.path）——表单是按点号路径定位输入框的。
func renameErrorFieldsToXboard(nodeType string, fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return fields
	}
	inverse := kernelToXboardField[nodeType]
	if len(inverse) == 0 {
		return fields
	}
	out := make(map[string]string, len(fields))
	for key, msg := range fields {
		if xboardName, ok := inverse[key]; ok {
			out[unflattenFieldPath(xboardName)] = msg
			continue
		}
		out[key] = msg
	}
	return out
}

// unflattenFieldPath 把 network_settings_headers_Host 还原成
// network_settings.headers.Host。
//
// 只在已知容器前缀处断开，不是见下划线就断：内核字段里 up_mbps、
// short_ids 这些本来就带下划线，无差别替换会得到 up.mbps 这种表单
// 根本找不到的路径。
func unflattenFieldPath(flat string) string {
	for container := range xboardContainers {
		prefix := container + "_"
		if len(flat) > len(prefix) && flat[:len(prefix)] == prefix {
			return container + "." + unflattenFieldPath(flat[len(prefix):])
		}
	}
	return flat
}
