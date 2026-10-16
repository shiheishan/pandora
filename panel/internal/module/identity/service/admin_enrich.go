package service

// 后台用户列表、详情里经端口补的字段（订阅、余额、订单、Telegram）；
// 端口定义在 admin_views.go / ops_types.go。列表一页每个端口只调一次（整页 user id）。

import (
	"context"

	"github.com/google/uuid"
)

// recentOrdersLimit 是详情里最近订单的条数。
const recentOrdersLimit = 20

// enrichRows 给一页用户补订阅与余额：AdminSubs、UserBalances 各调一次，空页不调。
func (s *Service) enrichRows(ctx context.Context, rows []UserRow) error {
	if len(rows) == 0 {
		return nil
	}
	subs, err := s.needSubs()
	if err != nil {
		return err
	}
	wallets, err := s.needWallets()
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	m, err := subs.AdminSubs(ctx, ids)
	if err != nil {
		return err
	}
	bal, err := wallets.UserBalances(ctx, ids)
	if err != nil {
		return err
	}
	for i := range rows {
		applySubs(&rows[i], m[rows[i].ID])
		rows[i].Balance = bal[rows[i].ID]
	}
	return nil
}

// applySubs 由订阅端口的结果填份数、当前订阅与生效套餐名。
func applySubs(r *UserRow, us AdminUserSubs) {
	r.SubscriptionCount = len(us.Subscriptions)
	r.Current = us.Current
	if us.Current != nil && us.Current.Live {
		name := us.Current.PlanName
		r.ActivePlan = &name
	} else {
		r.ActivePlan = nil
	}
}

// enrichDetail 给详情补订阅、余额、订单统计、最近订单与 Telegram 绑定，每个端口各调一次。
func (s *Service) enrichDetail(ctx context.Context, d *UserDetail) error {
	subs, err := s.needSubs()
	if err != nil {
		return err
	}
	wallets, err := s.needWallets()
	if err != nil {
		return err
	}
	orders, err := s.needOrders()
	if err != nil {
		return err
	}
	telegram, err := s.needTelegram()
	if err != nil {
		return err
	}
	uid := d.ID
	m, err := subs.AdminSubs(ctx, []uuid.UUID{uid})
	if err != nil {
		return err
	}
	us := m[uid]
	applySubs(&d.UserRow, us)
	if us.Subscriptions == nil {
		d.Subscriptions = []AdminSub{}
	} else {
		d.Subscriptions = us.Subscriptions
	}

	bal, err := wallets.UserBalances(ctx, []uuid.UUID{uid})
	if err != nil {
		return err
	}
	d.Balance = bal[uid]

	stats, err := orders.UserOrderStats(ctx, []uuid.UUID{uid})
	if err != nil {
		return err
	}
	st := stats[uid]
	d.OrderCount = st.Orders
	d.PaidTotal = st.Paid

	recent, err := orders.RecentOrders(ctx, uid, recentOrdersLimit)
	if err != nil {
		return err
	}
	if recent == nil {
		d.RecentOrders = []AdminOrderRow{}
	} else {
		d.RecentOrders = recent
	}

	d.Telegram, err = telegram.AdminTelegramBinding(ctx, uid)
	return err
}
