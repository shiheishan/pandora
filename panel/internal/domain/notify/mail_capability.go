package notify

import "context"

// mailReadiness 由「配置随数据库变化」的邮件发信器实现：配置齐了才算能发。
type mailReadiness interface {
	Configured(ctx context.Context) bool
}

// Configured 报告当前 SMTP 配置是否齐全（读的是 30 秒短缓存，与发信同一份）。
func (d *dynamicSender) Configured(ctx context.Context) bool {
	return d.provider.Config(ctx, d.tenantID).Enabled()
}

// EmailConfigured 报告本进程现在能不能往外发邮件：装了邮件发信器且 SMTP 配置齐全。
//
// 找回密码靠它决定开不开（用户 2026-10-07 定：没配邮件服务时隐藏入口）：验证码发不出去
// 时还让人走这条路，用户只会对着「验证码已发送」干等。只看配置、不看 notify.email 降级
// 开关：开关关着时信留在队列里、恢复后照发，入口不必跟着闪。
//
// tenantID 目前不参与判断：动态发信器在装配时就绑定了租户（首版单租户运行）。
func (s *Service) EmailConfigured(ctx context.Context, tenantID string) bool {
	_ = tenantID
	sender, ok := s.senders[ChannelEmail]
	if !ok || sender == nil {
		return false
	}
	// NewSMTPSender 配置不齐时返回 nil 指针；装进接口后不等于 nil，要单独认
	if ss, isStatic := sender.(*SMTPSender); isStatic && ss == nil {
		return false
	}
	if r, ok := sender.(mailReadiness); ok {
		return r.Configured(ctx)
	}
	// 静态 SMTPSender 只在配置齐全时才会被构造出来（NewSMTPSender 不齐就返回 nil）
	return true
}
