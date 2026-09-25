/**
 * [INPUT]: 依赖 ./harness 的 state / rawGet / runTable / Row，依赖后台各页面模块里页面实际使用的 zod schema（src/admin/**）
 * [OUTPUT]: 后台的形状冒烟：以平台管理员身份对后台前端调用的每个 GET 接口，用调用处的那个 schema 解析真实网关的响应；两个 CSV 导出与事件流只验状态与内容类型
 * [POS]: tests/smoke 的后台接口表，行序与 src 里的调用处一一对应（at 列即 file:line）；portal.smoke.ts 是门户的同构表
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { adminMeSchema } from '../../src/admin/me'
import { tasksSchema } from '../../src/admin/tasks'
import { adjustmentsSchema, latePaymentsSchema, orderResponseSchema, ordersSchema, paymentHistorySchema, providersSchema } from '../../src/admin/screens/billing/schemas'
import { announcementsResponse, pageResponse, pagesResponse, planCatalogResponse, siteSettingsSchema, slotsResponse, themesResponse } from '../../src/admin/screens/content/schemas'
import { activitySchema, backlogSchema, nodeTrafficSchema, overviewSchema, revenueSchema, systemStatusSchema, userTrafficSchema } from '../../src/admin/screens/dash/api'
import {
  batchesResponse,
  codesResponse,
  couponsResponse,
  giftStatsSchema,
  overviewSchema as commissionOverviewSchema,
  plansResponse as marketingPlansResponse,
  redemptionsResponse,
  templatesResponse as giftTemplatesResponse,
  usagesResponse,
  withdrawalsResponse,
} from '../../src/admin/screens/marketing/schemas'
import {
  globalRoutingSchema,
  identitySchema,
  metricsSchema,
  nodeRoutingSchema,
  nodesResponse,
  poolsResponse,
  protocolSchemasResponse,
  serverNodesResponse,
  serverSchema,
  serversResponse,
} from '../../src/admin/screens/nodes/schemas'
import { packsSchema, planPoolsSchema, planResponseSchema, plansSchema, poolOptionsSchema } from '../../src/admin/screens/plans/schemas'
import { accessResponse, auditResponse, clustersResponse, switchesResponse } from '../../src/admin/screens/security/schemas'
import { deliveriesResponse, hooksResponse, mailSettingsSchema, telegramSettingsSchema, templatesResponse as mailTemplatesResponse } from '../../src/admin/screens/system/schemas'
import { assigneesSchema, macrosSchema, queueSchema, ticketDetailSchema } from '../../src/admin/screens/tickets/api'
import { devicesSchema, planOptionsSchema, profileSchema, resetLogsSchema, resetStatsSchema, userDetailSchema, userGroupsSchema, usersSchema } from '../../src/admin/screens/users/api'
import { rawGet, runTable, state, type Row } from './harness'

const s = state.seed

// 种子里没有、要从列表里取的 id：列表响应原样读（不经 schema，schema 由对应行去验）
const kbPages = (await rawGet(state.admin, 'v1/content-pages?kind=kb_article&limit=500')) as { pages?: Array<{ id: string }> }
const batches = (await rawGet(state.admin, 'v1/gift-cards/batches?limit=50&offset=0')) as { items?: Array<{ id: string }> | null }
const pageId = kbPages.pages?.[0]?.id ?? 'missing'
const batchId = batches.items?.[0]?.id ?? 'missing'

const rows: Row[] = [
  // ---- 外框 ----
  { at: 'admin/me.ts:31', path: 'v1/me', schema: adminMeSchema },
  { at: 'admin/tasks.ts:45', path: 'v1/dashboard/tasks', schema: tasksSchema },

  // ---- 仪表盘 ----
  { at: 'dash/api.ts:212', seed: '定时扫描派发的支付通知', path: 'v1/dashboard/backlog/notifications', schema: backlogSchema },
  { at: 'dash/api.ts:222', path: 'v1/overview', schema: overviewSchema },
  { at: 'dash/api.ts:233', path: 'v1/revenue/timeseries', query: { currency: 'CNY', days: 30 }, schema: revenueSchema },
  { at: 'dash/api.ts:248', seed: 'UniProxy /push（节点上报流量）', path: 'v1/dashboard/traffic/nodes', query: { range: '24h', limit: 5 }, schema: nodeTrafficSchema },
  { at: 'dash/api.ts:260', seed: 'UniProxy /push（节点上报流量）', path: 'v1/dashboard/traffic/users', query: { range: '24h', limit: 5 }, schema: userTrafficSchema },
  { at: 'dash/api.ts:270', path: 'v1/system/status', schema: systemStatusSchema },
  { at: 'dash/api.ts:280', path: 'v1/stats/timeseries', query: { days: 14 }, schema: activitySchema },

  // ---- 工单 ----
  { at: 'tickets/api.ts:113', seed: '门户 POST support/tickets', path: 'v1/tickets', query: { status: 'open,pending_user,pending_agent,escalated', limit: 25, offset: 0 }, schema: queueSchema },
  { at: 'tickets/api.ts:129', path: `v1/tickets/${s.ticket_id}`, schema: ticketDetailSchema },
  { at: 'tickets/api.ts:139', seed: '种子管理员（aegis-adminctl）', path: 'v1/tickets/assignees', schema: assigneesSchema },
  { at: 'tickets/api.ts:149', seed: '后台 POST ticket-macros', path: 'v1/ticket-macros', schema: macrosSchema },

  // ---- 用户 ----
  { at: 'users/api.ts:233', seed: '门户注册两人 + 管理员', path: 'v1/users', query: { limit: 25, offset: 0 }, schema: usersSchema },
  { at: 'users/api.ts:243', path: `v1/users/${s.portal.user_id}`, schema: userDetailSchema },
  { at: 'users/api.ts:254', seed: '后台 POST user-groups', path: 'v1/user-groups', schema: userGroupsSchema },
  { at: 'users/api.ts:264', path: `v1/users/${s.portal.user_id}/profile`, schema: profileSchema },
  { at: 'users/api.ts:277', path: 'v1/users', query: { q: s.portal.email, limit: 100, offset: 0 }, schema: usersSchema },
  { at: 'users/api.ts:288', path: 'v1/plans', schema: planOptionsSchema },
  { at: 'users/api.ts:308', seed: '演示渠道付款开出订阅；UniProxy /alive 上报在线 IP', path: 'v1/devices', schema: devicesSchema },
  { at: 'users/api.ts:320', seed: '后台 POST users/{id}/traffic-reset', path: 'v1/traffic-resets', query: { limit: 25, offset: 0 }, schema: resetLogsSchema },
  { at: 'users/api.ts:329', path: 'v1/traffic-resets/stats', schema: resetStatsSchema },
  { at: 'users/api.ts:338', seed: '后台 POST users/{id}/traffic-reset', path: `v1/users/${s.portal.user_id}/traffic-resets`, schema: resetLogsSchema },

  // ---- 营销 ----
  { at: 'marketing/queries.ts:36', path: 'v1/plans', schema: marketingPlansResponse },
  { at: 'marketing/queries.ts:47', seed: '后台 POST coupons', path: 'v1/coupons', query: { limit: 50, offset: 0 }, schema: couponsResponse },
  { at: 'marketing/queries.ts:57', seed: '门户带 coupon_code 下单（兑换记录下单即落）', path: `v1/coupons/${s.coupon_id}/redemptions`, schema: redemptionsResponse },
  { at: 'marketing/queries.ts:67', seed: '后台 POST gift-cards', path: 'v1/gift-cards', schema: giftTemplatesResponse },
  { at: 'marketing/queries.ts:73', path: 'v1/gift-cards/stats', schema: giftStatsSchema },
  { at: 'marketing/queries.ts:80', seed: '后台 POST gift-cards/{id}/codes', path: 'v1/gift-cards/batches', query: { limit: 50, offset: 0 }, schema: batchesResponse },
  { at: 'marketing/queries.ts:89', seed: '后台 POST gift-cards/{id}/codes', path: 'v1/gift-cards/codes', query: { batch_id: batchId, limit: 50, offset: 0 }, schema: codesResponse },
  { at: 'marketing/queries.ts:99', seed: '门户 POST gift-cards/redeem', path: 'v1/gift-cards/usages', query: { template_id: s.gift_card_id }, schema: usagesResponse },
  { at: 'marketing/queries.ts:107', path: 'v1/commission/overview', schema: commissionOverviewSchema },
  { at: 'marketing/queries.ts:116', seed: 'SQL 夹具：佣金只由每小时的 SettleMatured 解冻，且同 IP 被标待复核、无接口解除', path: 'v1/withdrawals', schema: withdrawalsResponse },

  // ---- 节点 ----
  { at: 'nodes/queries.ts:24', seed: '接入 + POST activate', path: 'v1/nodes', query: { limit: 1000, include_retired: '1' }, schema: nodesResponse },
  { at: 'nodes/queries.ts:34', path: 'v1/node-protocol-schemas', schema: protocolSchemasResponse },
  { at: 'nodes/queries.ts:48', seed: '后台 POST servers', path: 'v1/servers', schema: serversResponse },
  // useServer 目前没有调用处（死 hook），接口仍验一遍
  { at: 'nodes/queries.ts:56', path: `v1/servers/${s.server_id}`, schema: serverSchema },
  { at: 'nodes/queries.ts:63', seed: '接入 + POST activate', path: `v1/servers/${s.server_id}/nodes`, schema: serverNodesResponse },
  { at: 'nodes/queries.ts:72', seed: '后台 POST node-pools', path: 'v1/node-pools', schema: poolsResponse },
  { at: 'nodes/queries.ts:80', path: `v1/nodes/${s.node_id}/identity`, schema: identitySchema },
  { at: 'nodes/queries.ts:88', seed: 'UniProxy /status', path: `v1/nodes/${s.node_id}/metrics`, query: { minutes: 1440 }, schema: metricsSchema },
  { at: 'nodes/queries.ts:95', seed: '后台 PUT nodes/{id}/routing', path: `v1/nodes/${s.node_id}/routing`, schema: nodeRoutingSchema },
  { at: 'nodes/queries.ts:101', seed: '后台 PUT nodes/routing（reauth + 幂等）', path: 'v1/nodes/routing', schema: globalRoutingSchema },

  // ---- 内容与外观 ----
  { at: 'content/queries.ts:21', seed: '后台 POST announcements', path: 'v1/announcements', schema: announcementsResponse },
  { at: 'content/queries.ts:34', seed: '后台 POST content-pages', path: 'v1/content-pages', query: { kind: 'kb_article', limit: 500 }, schema: pagesResponse },
  { at: 'content/queries.ts:47', path: `v1/content-pages/${pageId}`, schema: pageResponse },
  { at: 'content/queries.ts:58', path: 'v1/plans', schema: planCatalogResponse },
  { at: 'content/queries.ts:69', path: 'v1/themes', schema: themesResponse },
  { at: 'content/queries.ts:75', path: 'v1/slots', schema: slotsResponse },
  { at: 'content/queries.ts:81', path: 'v1/settings/site', schema: siteSettingsSchema },

  // ---- 套餐 ----
  { at: 'plans/api.ts:24', seed: '后台 POST plans/complete', path: 'v1/plans', schema: plansSchema },
  { at: 'plans/api.ts:33', path: `v1/plans/${s.plan_id}`, schema: planResponseSchema },
  { at: 'plans/api.ts:44', path: `v1/plans/${s.plan_id}/pools`, schema: planPoolsSchema },
  { at: 'plans/api.ts:55', path: 'v1/node-pools', schema: poolOptionsSchema },
  { at: 'plans/api.ts:65', seed: '后台 POST traffic-packs', path: 'v1/traffic-packs', schema: packsSchema },

  // ---- 系统 ----
  { at: 'system/queries.ts:18', path: 'v1/settings/mail', schema: mailSettingsSchema },
  { at: 'system/queries.ts:23', path: 'v1/settings/telegram', schema: telegramSettingsSchema },
  { at: 'system/queries.ts:28', path: 'v1/mail/templates', schema: mailTemplatesResponse },
  { at: 'system/queries.ts:33', seed: '后台 POST plugin-hooks', path: 'v1/plugin-hooks', schema: hooksResponse },
  {
    at: 'system/queries.ts:41', seed: '门户建工单触发 ticket.created，扫描器投到本机接收端',
    path: `v1/plugin-hooks/${s.hook_code}/deliveries`,
    schema: deliveriesResponse,
    // 投递行随工单同事务入队（queued），扫描器 20 秒后第一次、之后每 60 秒发出；等到有一条 sent
    waitFor: {
      ok: (d: unknown) => ((d as { deliveries?: Array<{ status?: string }> | null }).deliveries ?? []).some((x) => x.status === 'sent'),
      timeoutMs: 3 * 60_000,
      why: '3 分钟内没有一条投递变成 sent（接收端没收到或扫描器没跑）',
    },
  },

  // ---- 安全 ----
  { at: 'security/queries.ts:22', seed: '各写操作的审计', path: 'v1/audit', query: { limit: 50, offset: 0 }, schema: auditResponse },
  { at: 'security/queries.ts:33', seed: '登录与写操作的审计', path: 'v1/access-log', query: { limit: 50, offset: 0 }, schema: accessResponse },
  { at: 'security/queries.ts:43', seed: '三个账号都从 127.0.0.1 登录（同一 IP 哈希）', path: 'v1/ip-clusters', schema: clustersResponse },
  { at: 'security/queries.ts:52', path: 'v1/switches', schema: switchesResponse },

  // ---- 财务 ----
  { at: 'billing/api.ts:32', seed: '门户下单三张', path: 'v1/orders', query: { limit: 25, offset: 0 }, schema: ordersSchema },
  { at: 'billing/api.ts:42', path: `v1/orders/${s.order_id}`, schema: orderResponseSchema },
  { at: 'billing/api.ts:53', seed: '演示渠道回调两笔', path: `v1/orders/${s.order_id}/payments`, schema: paymentHistorySchema },
  { at: 'billing/api.ts:65', path: 'v1/users', query: { q: 'smoke', limit: 8, offset: 0 }, schema: usersSchema },
  { at: 'billing/api.ts:76', seed: '对已付订单再送一笔演示回调（excess_capture）', path: 'v1/late-payments', query: { limit: 25, offset: 0 }, schema: latePaymentsSchema },
  { at: 'billing/api.ts:85', seed: '迁移种子 offline + SQL 夹具 demo', path: 'v1/payment-providers', schema: providersSchema },
  { at: 'billing/api.ts:94', seed: '后台 POST revenue/adjustments', path: 'v1/revenue/adjustments', schema: adjustmentsSchema },

  // ---- 非 JSON：两个 CSV 导出与事件流，只验状态与内容类型 ----
  { at: 'security/AuditTab.tsx:167', path: 'v1/audit/export', query: { from: '2026-01-01', to: '2099-12-31' }, kind: 'raw' },
  { at: 'users/BulkTab.tsx:135', path: 'v1/users/bulk/export', query: { status: 'active' }, kind: 'raw' },
  { at: 'shell/runtime.tsx:96', path: 'v1/events', kind: 'sse' },
]

// 列表里没取到 id 时行照跑（路径里是 missing）：404 本身就是要报告的事
runTable('admin', rows)
