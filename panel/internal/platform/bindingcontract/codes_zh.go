package bindingcontract

// 失败码在面板上的中文文案（只在面板侧）。pdnd 上报的 detail 是补充说明，按原样显示在文案之后；
// 未知的码显示 UnknownFailureText 并带上原码，不当成错误拒收。

// FailureText 是节点失败码的中文文案。
var FailureText = map[string]string{
	FailurePortInUse:           "端口已被占用",
	FailurePortReserved:        "端口是本机保留端口，不能用于节点",
	FailureCertMissing:         "找不到节点证书",
	FailureCertInvalid:         "节点证书无效",
	FailureCertExpiring:        "节点证书即将过期",
	FailureProtocolUnsupported: "节点程序版本不支持该协议，请升级节点程序",
	FailureConfigInvalid:       "节点配置无效",
	FailureBindPermission:      "节点程序没有权限监听该端口",
}

// UpgradeFailureText 是节点程序升级失败码的中文文案。
var UpgradeFailureText = map[string]string{
	"upgrade_order_invalid":       "升级指令无效或已过期",
	"upgrade_release_unavailable": "取不到该版本的发布清单或程序",
	"upgrade_signature_invalid":   "发布清单的官方签名校验失败",
	"upgrade_hash_mismatch":       "下载的程序与发布清单的哈希不一致",
	"upgrade_downgrade_refused":   "目标版本不高于当前版本，已拒绝降级",
	"upgrade_install_failed":      "替换程序失败，仍在运行原版本",
	"upgrade_rolled_back":         "新版本启动后自检失败，已回滚到原版本",
}

// UnknownFailureText 是未知失败码的兜底文案，调用方在其后附上原码。
const UnknownFailureText = "节点上报了未知错误"

// FailureMessage 返回失败码的中文文案；未知码返回兜底文案与原码。
func FailureMessage(code string) string {
	if text, ok := FailureText[code]; ok {
		return text
	}
	if text, ok := UpgradeFailureText[code]; ok {
		return text
	}
	if !isCodeToken(code) {
		// 不规范的码不回显，免得把任意字符串带进后台页面
		return UnknownFailureText
	}
	return UnknownFailureText + "（" + code + "）"
}

// isCodeToken 判断失败码的形状：[a-z0-9_]，1 到 64 个字符。
func isCodeToken(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
