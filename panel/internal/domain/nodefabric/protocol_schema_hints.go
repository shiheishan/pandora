package nodefabric

// 协议 schema 里的表单说明与 REALITY 多值字段。单独成文件，免得
// protocol_schema.go 的字面量被说明文字淹没（对齐门按正则读那份字面量）。

// fallbackHint 是回落字段的说明：回落目标是什么、谁会被转过去。
const fallbackHint = "选填，host:port。回落目标是一个明文 HTTP 站点，认证失败的探测会被转过去，" +
	"让节点看起来像个普通网站；可以填本机（如 127.0.0.1:80 的本机 nginx），不能填内网地址。留空时回一个中性的 404 页面。"

const utlsHint = "客户端模仿的浏览器 TLS 指纹，订阅三种格式都会下发；留空按 chrome。"

const certPathHint = "绝对路径，放在 " + NodeCertificateDir + " 下，例如 " +
	NodeCertificateDir + "example.com/fullchain.pem（见 docs/node-certificates.md）。"

const keyPathHint = "绝对路径，放在 " + NodeCertificateDir + " 下，例如 " +
	NodeCertificateDir + "example.com/privkey.pem。"

// withCertHints 给带证书的协议补上 cert_path / key_path 的说明。
func withCertHints(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for key, value := range in {
		out[key] = value
	}
	out["cert_path"] = certPathHint
	out["key_path"] = keyPathHint
	return out
}

// withRealityHints 给 REALITY 的 dest / server_name / short_id 补说明。
func withRealityHints(in map[string]string) map[string]string {
	in["reality_settings.dest"] = "借用握手的真实公网站点，域名:端口，例如 www.example.com:443；不能填 IP、localhost 或内网域名。"
	in["reality_settings.server_name"] = "可填多个，用逗号分隔；每个用户的订阅按固定规则分到其中一个，分散特征。"
	in["reality_settings.short_id"] = "必填，可填多个（逗号分隔），每个不超过 16 位十六进制；每个用户的订阅分到其中一个。"
	return in
}

// withRealityListTypes 把 REALITY 的 server_name / short_id 标成 list：表单按
// 逗号分隔录入，一个值时存成字符串（与 xboard 同形），多个时存成数组。
// 翻译层（applyKernelShapeFixups）两种形状都认。
func withRealityListTypes(in map[string]string) map[string]string {
	in["reality_settings.server_name"] = "list"
	in["reality_settings.short_id"] = "list"
	return in
}
