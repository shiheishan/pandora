package service

// 本文件登记后台对用户的操作通知模板（用户 2026-10-21 定：后台换发链接、开待付单要通知用户；
// 加时长、加流量、重置流量不通知）。默认内容必须与迁移 00063 的种子逐字一致（TestSeedTemplatesMatchRegistry 对照）。

// TemplateLinkRotated 是管理员在后台给用户的某一份换链接后发给属主的通知（用户自己在用户端换不发）。
const TemplateLinkRotated = "subscription.link_rotated"

// TemplateOrderPending 是管理员在后台人工开单、结算方式选「待用户支付」后发给这个用户的通知（赠送、线下已收款不发）。
const TemplateOrderPending = "order.pending_created"

func init() {
	templateDefs = append(templateDefs,
		templateDef{
			Code:        TemplateLinkRotated,
			Channel:     ChannelInApp,
			Category:    categoryTransactional,
			Description: "管理员在后台给用户的某一份换链接后发一条给属主（用户自己在用户端换不发）",
			Allowed:     []string{"plan"},
			Subject:     "链接已更换",
			Body:        "管理员给你的「{{plan}}」换了新链接，旧链接已失效，用它的设备会连不上。请到「我的套餐」复制新链接，在 App 里重新添加；原来那条可以删掉。到期日和流量不变。",
		},
		templateDef{
			Code:        TemplateLinkRotated,
			Channel:     ChannelEmail,
			Category:    categoryTransactional,
			Description: "管理员在后台给用户的某一份换链接后发一封到属主的账号邮箱（用户自己在用户端换不发；不可退订）",
			Allowed:     []string{"site", "plan", "subs_url"},
			Subject:     "【{{site}}】你的套餐链接已更换",
			Body: `你好，

管理员给你的「{{plan}}」换了新链接，旧链接已失效，用它的设备会连不上。
请登录后到「我的套餐」复制新链接，在 App 里重新添加；原来那条可以删掉。到期日和流量不变。

{{subs_url}}

{{site}}`,
		},
		templateDef{
			Code:        TemplateLinkRotated,
			Channel:     ChannelTelegram,
			Category:    categoryTransactional,
			Description: "管理员在后台给用户的某一份换链接后发一条给已绑定 Telegram 的属主（用户自己在用户端换不发）",
			Allowed:     []string{"plan", "subs_url"},
			Subject:     "链接已更换",
			Body:        "管理员给你的「{{plan}}」换了新链接，旧链接已失效。请到「我的套餐」复制新链接，在 App 里重新添加。",
		},
		templateDef{
			Code:        TemplateOrderPending,
			Channel:     ChannelInApp,
			Category:    categoryTransactional,
			Description: "管理员在后台人工开单、选「待用户支付」后发一条给这个用户（赠送、线下已收款不发）",
			Allowed:     []string{"order_no", "plan", "amount", "expires_at"},
			Subject:     "有一张待付款订单",
			Body:        "管理员给你开了一张订单 {{order_no}}（「{{plan}}」），应付 {{amount}}。请在 {{expires_at}} 前到「订单」页付款，过期自动取消。",
		},
		templateDef{
			Code:        TemplateOrderPending,
			Channel:     ChannelEmail,
			Category:    categoryTransactional,
			Description: "管理员在后台人工开单、选「待用户支付」后发一封到这个用户的账号邮箱（赠送、线下已收款不发；不可退订）",
			Allowed:     []string{"site", "order_no", "plan", "amount", "expires_at", "order_url"},
			Subject:     "【{{site}}】你有一张待付款订单 {{order_no}}",
			Body: `你好，

管理员给你开了一张订单，等你付款：
订单号：{{order_no}}
套餐：{{plan}}
应付：{{amount}}
付款期限：{{expires_at}}

过期自动取消。登录后到「订单」页付款：
{{order_url}}

{{site}}`,
		},
		templateDef{
			Code:        TemplateOrderPending,
			Channel:     ChannelTelegram,
			Category:    categoryTransactional,
			Description: "管理员在后台人工开单、选「待用户支付」后发一条给已绑定 Telegram 的这个用户（赠送、线下已收款不发）",
			Allowed:     []string{"order_no", "plan", "amount", "expires_at", "order_url"},
			Subject:     "有一张待付款订单",
			Body:        "管理员给你开了一张订单 {{order_no}}（「{{plan}}」），应付 {{amount}}，请在 {{expires_at}} 前付款，过期自动取消。去付款：{{order_url}}",
		},
	)
	sampleVars["subs_url"] = "https://panel.example.test/#/subs"
	sampleVars["order_url"] = "https://panel.example.test/#/orders/AO20260804-XXXXXX"
}
