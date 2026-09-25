/**
 * [INPUT]: 依赖 ./harness 的 state / runTable / Row，依赖门户各页面模块里页面实际使用的 zod schema（src/portal/**）
 * [OUTPUT]: 门户的形状冒烟：以种子门户用户身份对门户前端调用的每个 GET 接口，用调用处的那个 schema 解析真实网关的响应；事件流只验状态与内容类型
 * [POS]: tests/smoke 的门户接口表，与 admin.smoke.ts 同构；调用处内联的外层 z.object 在这里照原样写一遍，里面的行 schema 一律从页面模块导入
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { z } from 'zod'
import { appearanceSchema, balanceSchema, commissionSchema, meSchema, notificationsSchema, siteConfigSchema, subscriptionsSchema } from '../../src/portal/queries'
import { preferencesSchema, sessionSchema, telegramSchema } from '../../src/portal/screens/account/api'
import { listSchema as announcementListSchema } from '../../src/portal/screens/common/announcements'
import { packSchema, paymentMethodSchema, plansSchema } from '../../src/portal/screens/common/catalog'
import { orderDetailSchema, ordersPageSchema } from '../../src/portal/screens/common/orders'
import { linksSchema, nodesSchema, trafficPacksSchema, usageReportSchema } from '../../src/portal/screens/common/subscriptions'
import { contentDetailSchema, contentPageSchema, HELP_QUERY } from '../../src/portal/screens/help/api'
import { NOTIFICATION_LIMIT, notificationSchema } from '../../src/portal/screens/messages/api'
import { inviteSchema } from '../../src/portal/screens/referral/api'
import { ticketDetailSchema, ticketRowSchema } from '../../src/portal/screens/tickets/api'
import { redemptionSchema } from '../../src/portal/screens/wallet/api'
import { runTable, state, type Row } from './harness'

const s = state.seed

// 通知只由 aegis-public 的定时扫描写入（启动约 30 秒后第一次，之后每 5 分钟，写死在 cmd/aegis-public/main.go）。
// 等到非空或超时：6 分钟覆盖「第一次扫描早于付款」时的下一轮
const notifications = {
  ok: (d: unknown) => ((d as { notifications?: unknown[] }).notifications?.length ?? 0) > 0,
  timeoutMs: 6 * 60_000,
  why: '6 分钟内通知列表仍为空（只由定时扫描写入），空数组验不出行 schema',
}

const rows: Row[] = [
  // ---- 外框 ----
  { at: 'portal/queries.ts:54', path: 'v1/site-config', auth: false, schema: siteConfigSchema },
  { at: 'portal/queries.ts:63', path: 'v1/appearance', auth: false, schema: appearanceSchema },
  { at: 'portal/queries.ts:70', path: 'v1/me', schema: meSchema },
  { at: 'portal/queries.ts:82', path: 'v1/me/balance', schema: balanceSchema },
  { at: 'portal/queries.ts:160', path: 'v1/me/subscriptions', schema: subscriptionsSchema },
  { at: 'portal/queries.ts:231', path: 'v1/me/commission', schema: commissionSchema },
  { at: 'portal/queries.ts:248', path: 'v1/me/notifications', query: { limit: 1 }, schema: notificationsSchema, waitFor: notifications },

  // ---- 套餐与下单 ----
  { at: 'common/catalog.ts:26', path: 'v1/plans', schema: plansSchema },
  { at: 'common/catalog.ts:49', path: 'v1/traffic-packs', schema: z.object({ packs: z.array(packSchema) }) },
  { at: 'common/catalog.ts:67', path: 'v1/payment-methods', schema: z.object({ methods: z.array(paymentMethodSchema) }) },

  // ---- 订阅 ----
  { at: 'common/subscriptions.ts:42', path: 'v1/me/subscription-links', schema: linksSchema },
  { at: 'common/subscriptions.ts:83', path: `v1/me/subscriptions/${s.subscription_id}/nodes`, schema: nodesSchema },
  { at: 'common/subscriptions.ts:106', path: `v1/me/subscriptions/${s.subscription_id}/usage`, schema: usageReportSchema },
  { at: 'common/subscriptions.ts:133', path: 'v1/me/traffic-packs', schema: trafficPacksSchema },

  // ---- 订单 ----
  { at: 'common/orders.ts:99', path: 'v1/orders', query: { status: 'pending_payment' }, schema: ordersPageSchema },
  { at: 'common/orders.ts:137', path: `v1/orders/${s.order_id}`, schema: orderDetailSchema },
  { at: 'common/orders.ts:184', path: `v1/orders/${s.pending_order_id}`, schema: orderDetailSchema },
  { at: 'common/orders.ts:215', path: 'v1/orders', query: { status: 'paid,fulfilled,cancelled,expired,partially_refunded,refunded', limit: 6, offset: 0 }, schema: ordersPageSchema },
  { at: 'common/orders.ts:230', path: 'v1/orders', query: { status: 'draft,pending_payment,processing', limit: 20 }, schema: ordersPageSchema },

  // ---- 工单 ----
  { at: 'tickets/api.ts:66', path: 'v1/support/categories', schema: z.object({ categories: z.array(z.object({ code: z.string(), name: z.string() })) }) },
  { at: 'tickets/api.ts:77', path: 'v1/support/tickets', schema: z.object({ tickets: z.array(ticketRowSchema) }) },
  { at: 'tickets/api.ts:87', path: `v1/support/tickets/${s.ticket_id}`, schema: ticketDetailSchema },
  { at: 'tickets/api.ts:98', path: 'v1/orders', query: { limit: 20 }, schema: ordersPageSchema },

  // ---- 消息、推荐、钱包、账号、帮助 ----
  {
    at: 'messages/api.ts:31',
    path: 'v1/me/notifications',
    query: { limit: NOTIFICATION_LIMIT },
    schema: z.object({ notifications: z.array(notificationSchema), unread: z.number().int() }),
    waitFor: notifications,
  },
  { at: 'common/announcements.ts:33', path: 'v1/me/announcements', schema: announcementListSchema },
  { at: 'referral/api.ts:26', path: 'v1/me/invite', schema: inviteSchema },
  { at: 'wallet/api.ts:104', path: 'v1/me/gift-cards', schema: z.object({ redemptions: z.array(redemptionSchema) }) },
  { at: 'account/api.ts:36', path: 'v1/me/sessions', schema: z.object({ sessions: z.array(sessionSchema) }) },
  { at: 'account/api.ts:83', path: 'v1/me/telegram', schema: telegramSchema },
  { at: 'account/api.ts:119', path: 'v1/me/notification-preferences', schema: preferencesSchema },
  { at: 'help/api.ts:41', path: 'v1/content/pages', query: { ...HELP_QUERY }, schema: z.object({ pages: z.array(contentPageSchema) }) },
  { at: 'help/api.ts:52', path: `v1/content/pages/${s.page_slug}`, query: { ...HELP_QUERY }, schema: contentDetailSchema },

  // ---- 事件流 ----
  { at: 'shell/runtime.tsx:96', path: 'v1/events', kind: 'sse' },
]

runTable('portal', rows)
