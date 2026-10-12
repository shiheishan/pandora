package service

// 内置模板登记表：模板代码是代码里的常量，后台只能改内容，不能新建（建一个没人发送的 code 只会误导人）。
// 默认内容与迁移 00007_notify.sql 的种子逐字一致，「恢复默认」回到这里；改一处必须同改另一处（PG 测试对照）。

// 渠道。P2 只发邮件。
const ChannelEmail = "email"

// 模板代码。
const (
	TemplateEmailVerify   = "auth.email_verify"
	TemplatePasswordReset = "auth.password_reset"
)

// 模板类别：事务类不可退订。
const categoryTransactional = "transactional"

// templateLocale 与 templateStatus 是对外 DTO 里的固定值（表里不再存）。
const (
	templateLocale = "zh-CN"
	templateStatus = "active"
)

// templateDef 是一个内置模板的定义。
type templateDef struct {
	Code        string
	Channel     string
	Category    string
	Description string   // 后台显示的「什么时候发、发给谁」
	Allowed     []string // 变量白名单，渲染只认这些名字
	Subject     string   // 默认主题
	Body        string   // 默认正文
}

var templateDefs = []templateDef{
	{
		Code:        TemplateEmailVerify,
		Channel:     ChannelEmail,
		Category:    categoryTransactional,
		Description: "注册第 1 步发给注册邮箱的验证码（开启邮箱验证时；邮箱已注册则不发）",
		Allowed:     []string{"site", "code", "minutes"},
		Subject:     "【{{site}}】注册验证码 {{code}}",
		Body: `你好，

你正在注册 {{site}}，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。如果这不是你本人的操作，忽略这封邮件即可。

{{site}}`,
	},
	{
		Code:        TemplatePasswordReset,
		Channel:     ChannelEmail,
		Category:    categoryTransactional,
		Description: "找回密码第 1 步发给账号邮箱的验证码（邮箱不存在或是后台人员时不发）",
		Allowed:     []string{"site", "code", "minutes"},
		Subject:     "【{{site}}】重置密码验证码 {{code}}",
		Body: `你好，

你正在重置 {{site}} 的登录密码，验证码是：

{{code}}

验证码 {{minutes}} 分钟内有效。重置成功后，这个账号在所有设备上的登录都会失效，需要用新密码重新登录。

如果这不是你本人的操作，忽略这封邮件即可，你的密码不会改变。

{{site}}`,
	},
}

// lookupDef 按 code 与渠道找内置模板定义。
func lookupDef(code, channel string) (templateDef, bool) {
	for _, d := range templateDefs {
		if d.Code == code && d.Channel == channel {
			return d, true
		}
	}
	return templateDef{}, false
}
