package service

// 本文件放在单独文件里是为了不动 registry.go；
// 默认内容必须与迁移 00040 的种子逐字一致（TestSeedTemplatesMatchRegistry 会对照）。

// TemplateLoginLink 是邮件链接登录的邮件（auth 发送；迁移 00040 种了同样内容的种子）。
const TemplateLoginLink = "auth.login_link"

func init() {
	templateDefs = append(templateDefs, templateDef{
		Code:        TemplateLoginLink,
		Channel:     ChannelEmail,
		Category:    categoryTransactional,
		Description: "门户「邮件链接登录」发给账号邮箱的一次性登录链接（邮箱不存在、账号不可用或是管理员时不发）",
		Allowed:     []string{"site", "link", "minutes"},
		Subject:     "【{{site}}】登录链接",
		Body:        "你好，\n\n点击下面的链接登录 {{site}}：\n\n{{link}}\n\n链接 {{minutes}} 分钟内有效，只能用一次。如果这不是你本人的操作，忽略这封邮件即可，你的账号不会有任何变化。\n\n{{site}}",
	})
	sampleVars["link"] = "https://panel.example.test/#/mail-login/示例令牌"
}
