package service

// 内置模板登记表：模板代码是代码里的常量，后台只能改内容，不能新建（建一个没人发送的 code 只会误导人）。
// 默认内容与迁移 00007_notify.sql、00026_notify_inbox.sql 的种子逐字一致，「恢复默认」回到这里；
// 改一处必须同改另一处（PG 测试对照）。

// 渠道：邮件与 Telegram 经投递表派发；站内信写进 notification_inbox 即送达。
const (
	ChannelEmail    = "email"
	ChannelInApp    = "inapp"
	ChannelTelegram = "telegram"
)

// 模板代码。
const (
	TemplateEmailVerify          = "auth.email_verify"
	TemplatePasswordReset        = "auth.password_reset"
	TemplateSubscriptionExpiring = "subscription.expiring"
	TemplateSubscriptionExpired  = "subscription.expired"
	TemplateSubscriptionRecall   = "subscription.recall"
	TemplateQuotaWarning         = "quota.warning"
	TemplateOrderPaid            = "order.paid"
	TemplateTicketReplied        = "ticket.replied"
)

// 模板类别：transactional 不可退订；service 是服务提醒，用户可在通知偏好里按渠道关闭。
const (
	categoryTransactional = "transactional"
	categoryService       = "service"
)

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

	// ---- 到期提醒（notify:scan）：剩 7、3、1 天各一条 ----
	{
		Code:        TemplateSubscriptionExpiring,
		Channel:     ChannelInApp,
		Category:    categoryService,
		Description: "订阅剩 7、3、1 天到期时各发一条（定时扫描）",
		Allowed:     []string{"plan", "days", "expires_at"},
		Subject:     "套餐即将到期",
		Body:        "你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期。到期后节点会停止服务，记得及时续费。",
	},
	{
		Code:        TemplateSubscriptionExpiring,
		Channel:     ChannelEmail,
		Category:    categoryService,
		Description: "订阅剩 7、3、1 天到期时各发一封（定时扫描；用户可在通知偏好里关闭）",
		Allowed:     []string{"site", "plan", "days", "expires_at"},
		Subject:     "【{{site}}】你的套餐 {{days}} 天后到期",
		Body: `你好，

你的「{{plan}}」将在 {{days}} 天后到期（{{expires_at}}）。
到期后节点将停止服务，请及时续费以免影响使用。

{{site}}`,
	},
	{
		Code:        TemplateSubscriptionExpiring,
		Channel:     ChannelTelegram,
		Category:    categoryService,
		Description: "订阅剩 7、3、1 天到期时各发一条给已绑定 Telegram 的用户（定时扫描）",
		Allowed:     []string{"plan", "days", "expires_at"},
		Subject:     "套餐即将到期",
		Body:        "你的「{{plan}}」将在 {{days}} 天后（{{expires_at}}）到期，记得续费。",
	},
	// ---- 到期当时（notify:scan）----
	{
		Code:        TemplateSubscriptionExpired,
		Channel:     ChannelInApp,
		Category:    categoryService,
		Description: "订阅到期后一天内发一条（定时扫描）",
		Allowed:     []string{"plan", "expired_at"},
		Subject:     "套餐已到期",
		Body:        "你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。续费后在客户端里更新订阅即可恢复，订阅链接不变。",
	},
	{
		Code:        TemplateSubscriptionExpired,
		Channel:     ChannelEmail,
		Category:    categoryService,
		Description: "订阅到期后一天内发一封（定时扫描；用户可在通知偏好里关闭）",
		Allowed:     []string{"site", "plan", "expired_at"},
		Subject:     "【{{site}}】你的套餐已到期",
		Body: `你好，

你的「{{plan}}」已于 {{expired_at}} 到期，节点已停止服务。
续费后在客户端里更新一次订阅即可恢复，订阅链接不变，无需重新导入。
过期满 30 天后将不能再原地续费，只能重新购买并更换订阅链接。

{{site}}`,
	},
	{
		Code:        TemplateSubscriptionExpired,
		Channel:     ChannelTelegram,
		Category:    categoryService,
		Description: "订阅到期后一天内发一条给已绑定 Telegram 的用户（定时扫描）",
		Allowed:     []string{"plan", "expired_at"},
		Subject:     "套餐已到期",
		Body:        "你的「{{plan}}」已于 {{expired_at}} 到期，续费后更新订阅即可恢复，链接不变。",
	},
	// ---- 过期召回（notify:scan）：过期第 1、7 天，名下没有别的生效订阅才发 ----
	{
		Code:        TemplateSubscriptionRecall,
		Channel:     ChannelInApp,
		Category:    categoryService,
		Description: "订阅过期第 1、7 天各发一条，名下有别的生效订阅时不发（定时扫描）",
		Allowed:     []string{"plan", "expired_at", "days"},
		Subject:     "套餐已过期 {{days}} 天",
		Body:        "你的「{{plan}}」已于 {{expired_at}} 到期。现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买。",
	},
	{
		Code:        TemplateSubscriptionRecall,
		Channel:     ChannelEmail,
		Category:    categoryService,
		Description: "订阅过期第 1、7 天各发一封，名下有别的生效订阅时不发（定时扫描；用户可在通知偏好里关闭）",
		Allowed:     []string{"site", "plan", "expired_at", "days"},
		Subject:     "【{{site}}】你的套餐已过期 {{days}} 天",
		Body: `你好，

你的「{{plan}}」已于 {{expired_at}} 到期，至今已 {{days}} 天。
现在续费，原订阅链接自动恢复，无需重新导入；过期满 30 天后只能重新购买，并需要更换订阅链接。

{{site}}`,
	},
	{
		Code:        TemplateSubscriptionRecall,
		Channel:     ChannelTelegram,
		Category:    categoryService,
		Description: "订阅过期第 1、7 天各发一条给已绑定 Telegram 的用户，名下有别的生效订阅时不发（定时扫描）",
		Allowed:     []string{"plan", "expired_at", "days"},
		Subject:     "套餐已过期 {{days}} 天",
		Body:        "你的「{{plan}}」已于 {{expired_at}} 到期，现在续费原链接自动恢复。",
	},
	// ---- 流量预警（notify:scan）：本期用量到 80%、95% 各一条 ----
	{
		Code:        TemplateQuotaWarning,
		Channel:     ChannelInApp,
		Category:    categoryService,
		Description: "本期流量（含挂在这一份上的流量包）用到 80%、95% 时各发一条（定时扫描）",
		Allowed:     []string{"plan", "percent", "remaining"},
		Subject:     "流量即将用尽",
		Body:        "你的「{{plan}}」已使用 {{percent}}% 流量（剩余 {{remaining}}）。用尽后将无法连接节点。",
	},
	{
		Code:        TemplateQuotaWarning,
		Channel:     ChannelEmail,
		Category:    categoryService,
		Description: "本期流量（含挂在这一份上的流量包）用到 80%、95% 时各发一封（定时扫描；用户可在通知偏好里关闭）",
		Allowed:     []string{"site", "plan", "percent", "remaining"},
		Subject:     "【{{site}}】流量已使用 {{percent}}%",
		Body: `你好，

你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。
流量用尽后将无法连接节点，可在面板购买流量包或升级套餐。

{{site}}`,
	},
	{
		Code:        TemplateQuotaWarning,
		Channel:     ChannelTelegram,
		Category:    categoryService,
		Description: "本期流量用到 80%、95% 时各发一条给已绑定 Telegram 的用户（定时扫描）",
		Allowed:     []string{"plan", "percent", "remaining"},
		Subject:     "流量预警",
		Body:        "你的「{{plan}}」已使用 {{percent}}% 流量，剩余 {{remaining}}。",
	},
	// ---- 支付成功：订单结算时由 order 经 NotifyUser 在结算事务里写 ----
	{
		Code:        TemplateOrderPaid,
		Channel:     ChannelInApp,
		Category:    categoryTransactional,
		Description: "订单支付成功、套餐开通后发一条（订单结算时写入）",
		Allowed:     []string{"order_no", "plan", "expires_at"},
		Subject:     "支付成功",
		Body:        "订单 {{order_no}} 已支付成功，「{{plan}}」已开通，有效期至 {{expires_at}}。",
	},
	{
		Code:        TemplateOrderPaid,
		Channel:     ChannelTelegram,
		Category:    categoryTransactional,
		Description: "订单支付成功、套餐开通后发一条给已绑定 Telegram 的用户（订单结算时写入）",
		Allowed:     []string{"order_no", "plan", "expires_at"},
		Subject:     "支付成功",
		Body:        "订单 {{order_no}} 已支付，「{{plan}}」已开通，有效期至 {{expires_at}}。",
	},
	// ---- 工单回复：客服回复工单时由 support 经 NotifyUser 写 ----
	{
		Code:        TemplateTicketReplied,
		Channel:     ChannelInApp,
		Category:    categoryService,
		Description: "客服回复工单后发一条给提交人",
		Allowed:     []string{"subject"},
		Subject:     "工单有新回复",
		Body:        "你的工单「{{subject}}」有新回复，点击查看。",
	},
	{
		Code:        TemplateTicketReplied,
		Channel:     ChannelTelegram,
		Category:    categoryService,
		Description: "客服回复工单后发一条给已绑定 Telegram 的提交人",
		Allowed:     []string{"subject"},
		Subject:     "工单有新回复",
		Body:        "你的工单「{{subject}}」有新回复。",
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
