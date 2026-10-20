package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// SiteChangeListener 在站点设置（site 组）提交后被同步调用：appearance 据此清入口页快照（PageMeta）。
type SiteChangeListener interface{ SiteSettingsChanged() }

// SubscriptionExtrasListener 在订阅附加设置提交后被同步调用：subscription 据此清拉取快照。
type SubscriptionExtrasListener interface{ SubscriptionExtrasChanged() }

// UseSiteChangeListener 接上站点设置变更的监听方（启动装配时调用，不并发安全）。
func (s *Service) UseSiteChangeListener(l SiteChangeListener) { s.siteListener = l }

// UseSubscriptionExtrasListener 接上订阅附加设置变更的监听方（启动装配时调用，不并发安全）。
func (s *Service) UseSubscriptionExtrasListener(l SubscriptionExtrasListener) { s.extrasListener = l }

// siteChanged 在站点设置事务提交后通知监听方；回滚不通知。serve 单实例、后台写都在 serve 里，进程内清就够；
// ctx 里没有事务时 AfterCommit 会立即执行。
func (s *Service) siteChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "settings:site_saved", func(context.Context) {
		if s.siteListener != nil {
			s.siteListener.SiteSettingsChanged()
		}
	})
}

// subscriptionExtrasChanged 在订阅附加设置事务提交后通知监听方；回滚不通知。serve 单实例、后台写都在 serve 里，进程内清就够；
// ctx 里没有事务时 AfterCommit 会立即执行。
func (s *Service) subscriptionExtrasChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "settings:subscription_extras_saved", func(context.Context) {
		if s.extrasListener != nil {
			s.extrasListener.SubscriptionExtrasChanged()
		}
	})
}
