package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/repo"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	"github.com/google/uuid"
)

// paidCursor 是支付成功通知的游标名（notify_scan_cursors.name）。
const paidCursor = "order_paid"

// scanPaid 按 (履约时间, 订单 id) 游标翻一页（至多 scanBatch 张）已履约订单写支付成功通知，写完推进游标：
// 停机多久都不漏，高峰也不会被已通知过的单占住名额。中途有一件写失败时游标只推进到它前面，
// 下一轮从它重来（已写的靠去重键跳过）。还没有游标时从 paidLookback 之前开始。
//
// 套餐与到期时间取订单挂的订阅在扫描时的值：几分钟内连续续费或换套餐时显示的是最新一期，可以接受。
//
// 流量重置单不给用户写通知（没有专门模板，门户下单页当场显示结果）；收款提醒开着时给管理员写一条，
// 套餐一栏前缀「流量重置 · 」（用户 10-14 定），关着时整单跳过、游标照样推进。
func (s *Service) scanPaid(ctx context.Context, now time.Time, loc *time.Location) (int, error) {
	cur, ok, err := s.store.ScanCursor(ctx, paidCursor)
	if err != nil {
		return 0, err
	}
	if !ok {
		cur = repo.ScanCursor{At: now.Add(-paidLookback)}
	}
	paid, err := s.orders.PaidForNotify(ctx, cur.At, cur.ID, scanBatch)
	if err != nil || len(paid) == 0 {
		return 0, err
	}

	var adminChatID int64
	var emails map[uuid.UUID]string
	ex, err := s.settings.TelegramExtras(ctx)
	if err != nil {
		return 0, err
	}
	if ex.AdminPaidNotify {
		cid, ok, err := s.adminChat(ctx)
		if err != nil {
			return 0, err
		}
		if ok {
			adminChatID = cid
			uids := make([]uuid.UUID, 0, len(paid))
			seenUser := map[uuid.UUID]bool{}
			for _, p := range paid {
				if !seenUser[p.UserID] {
					seenUser[p.UserID] = true
					uids = append(uids, p.UserID)
				}
			}
			emails, err = s.dir.Emails(ctx, uids)
			if err != nil {
				return 0, err
			}
		}
	}

	ids := make([]uuid.UUID, len(paid))
	for i, p := range paid {
		ids[i] = p.SubscriptionID
	}
	subs, err := s.subs.SubscriptionsByIDs(ctx, ids)
	if err != nil {
		return 0, err
	}
	items := make([]scanItem, 0, len(paid))
	// owner[i] 是 items[i] 对应的 paid 下标；订阅已不存在的单没有条目，直接算处理完
	owner := make([]int, 0, len(paid))
	// reset[i] 是 items[i] 是否流量重置单：这类单只写管理员收款提醒，不给用户写通知（用户 10-14 定）
	reset := make([]bool, 0, len(paid))
	for i, p := range paid {
		// 流量重置单不是「套餐已开通」，没有给用户的专门模板（门户下单页当场显示结果）；
		// 管理员收款提醒照发，套餐一栏前缀「流量重置 · 」。收款提醒关着（或没配群组）时不写，算处理完。
		if p.Kind == ordersvc.KindTrafficReset && adminChatID == 0 {
			continue
		}
		sub, ok := subs[p.SubscriptionID]
		if !ok {
			continue
		}
		it := scanItem{
			UserID: p.UserID,
			Code:   TemplateOrderPaid,
			Vars: map[string]string{
				"order_no":   p.OrderNo,
				"plan":       sub.PlanID.String(),
				"expires_at": noticeTime(sub.PeriodEnd, loc),
			},
			Key: "order-paid:" + p.OrderID.String(),
		}
		// 流量包订单不是「套餐已开通」，换流量包的文案（用户 10-13 定）
		if p.Kind == ordersvc.KindAddon && p.TrafficBytes != nil {
			it.Code = TemplatePackPaid
			it.Vars = map[string]string{
				"order_no": p.OrderNo,
				"plan":     sub.PlanID.String(),
				"traffic":  humanBytes(*p.TrafficBytes),
			}
		}
		isReset := p.Kind == ordersvc.KindTrafficReset
		if isReset {
			// 只写管理员提醒的条目：Code 留空，Key 用管理员提醒的去重键；
			// Vars 给空 map（resolvePlanNames 会往里写，nil map 会 panic）
			it = scanItem{
				UserID: p.UserID,
				Code:   "",
				Vars:   map[string]string{},
				Key:    "admin-paid:" + p.OrderID.String(),
			}
		}
		if adminChatID != 0 {
			it.Admin = &adminNotice{
				Code: TemplateAdminOrderPaid,
				Vars: map[string]string{
					"order_no": p.OrderNo,
					"amount":   paidAmount(p),
					"plan":     sub.PlanID.String(),
					"email":    maskEmail(emails[p.UserID]),
				},
				Key:     "admin-paid:" + p.OrderID.String(),
				AboutID: p.UserID,
			}
		}
		items = append(items, it)
		owner = append(owner, i)
		reset = append(reset, isReset)
	}
	if err := s.resolvePlanNames(ctx, items); err != nil {
		return 0, err
	}
	// 重置单的管理员提醒：套餐一栏带「流量重置 · 」前缀（模板正文与变量不改，只改传进去的 plan 值）
	for i, r := range reset {
		if r {
			items[i].Admin.Vars["plan"] = "流量重置 · " + items[i].Admin.Vars["plan"]
		}
	}
	written, done, werr := s.writeScanItems(ctx, items, adminChatID)
	last := len(paid) - 1
	if done < len(items) {
		last = owner[done] - 1
	}
	if last >= 0 {
		next := repo.ScanCursor{At: paid[last].FulfilledAt, ID: paid[last].OrderID}
		if err := s.store.SetScanCursor(ctx, paidCursor, next); err != nil {
			return written, err
		}
	}
	return written, werr
}
