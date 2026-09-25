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
const batches = (await rawGet(state.admin, 'v1/gift-cards/batches?limit=50&offset=0')) as { batches?: Array<{ id: string }> }
const pageId = kbPages.pages?.[0]?.id ?? 'missing'
const batchId = batches.batches?.[0]?.id ?? 'missing'

const rows: Row[] = [
  // ---- 外框 ----
  { at: 'admin/me.ts:31', path: 'v1/me', schema: adminMeSchema },
  { at: 'admin/tasks.ts:45', path: 'v1/dashboard/tasks', schema: tasksSchema },

  // ---- 仪表盘 ----
  { at: 'dash/api.ts:212', path: 'v1/dashboard/backlog/notifications', schema: backlogSchema },
  { at: 'dash/api.ts:222', path: 'v1/overview', schema: overviewSchema },
  { at: 'dash/api.ts:233', path: 'v1/revenue/timeseries', query: { currency: 'CNY', days: 30 }, schema: revenueSchema },
  { at: 'dash/api.ts:248', path: 'v1/dashboard/traffic/nodes', query: { range: '24h', limit: 5 }, schema: nodeTrafficSchema },
  { at: 'dash/api.ts:260', path: 'v1/dashboard/traffic/users', query: { range: '24h', limit: 5 }, schema: userTrafficSchema },
  { at: 'dash/api.ts:270', path: 'v1/system/status', schema: systemStatusSchema },
  { at: 'dash/api.ts:280', path: 'v1/stats/timeseries', query: { days: 14 }, schema: activitySchema },

  // ---- 工单 ----
  { at: 'tickets/api.ts:113', path: 'v1/tickets', query: { status: 'open,pending_user,pending_agent,escalated', limit: 25, offset: 0 }, schema: queueSchema },
  { at: 'tickets/api.ts:129', path: `v1/tickets/${s.ticket_id}`, schema: ticketDetailSchema },
  { at: 'tickets/api.ts:139', path: 'v1/tickets/assignees', schema: assigneesSchema },
  { at: 'tickets/api.ts:149', path: 'v1/ticket-macros', schema: macrosSchema },

  // ---- 用户 ----
  { at: 'users/api.ts:233', path: 'v1/users', query: { limit: 25, offset: 0 }, schema: usersSchema },
  { at: 'users/api.ts:243', path: `v1/users/${s.portal.user_id}`, schema: userDetailSchema },
  { at: 'users/api.ts:254', path: 'v1/user-groups', schema: userGroupsSchema },
  { at: 'users/api.ts:264', path: `v1/users/${s.portal.user_id}/profile`, schema: profileSchema },
  { at: 'users/api.ts:277', path: 'v1/users', query: { q: s.portal.email, limit: 100, offset: 0 }, schema: usersSchema },
  { at: 'users/api.ts:288', path: 'v1/plans', schema: planOptionsSchema },
  { at: 'users/api.ts:308', path: 'v1/devices', schema: devicesSchema },
  { at: 'users/api.ts:320', path: 'v1/traffic-resets', query: { limit: 25, offset: 0 }, schema: resetLogsSchema },
  { at: 'users/api.ts:329', path: 'v1/traffic-resets/stats', schema: resetStatsSchema },
  { at: 'users/api.ts:338', path: `v1/users/${s.portal.user_id}/traffic-resets`, schema: resetLogsSchema },

  // ---- 营销 ----
  { at: 'marketing/queries.ts:36', path: 'v1/plans', schema: marketingPlansResponse },
  { at: 'marketing/queries.ts:47', path: 'v1/coupons', query: { limit: 50, offset: 0 }, schema: couponsResponse },
  { at: 'marketing/queries.ts:57', path: `v1/coupons/${s.coupon_id}/redemptions`, schema: redemptionsResponse },
  { at: 'marketing/queries.ts:67', path: 'v1/gift-cards', schema: giftTemplatesResponse },
  { at: 'marketing/queries.ts:73', path: 'v1/gift-cards/stats', schema: giftStatsSchema },
  { at: 'marketing/queries.ts:80', path: 'v1/gift-cards/batches', query: { limit: 50, offset: 0 }, schema: batchesResponse },
  { at: 'marketing/queries.ts:89', path: 'v1/gift-cards/codes', query: { batch_id: batchId, limit: 50, offset: 0 }, schema: codesResponse },
  { at: 'marketing/queries.ts:99', path: 'v1/gift-cards/usages', query: { template_id: s.gift_card_id }, schema: usagesResponse },
  { at: 'marketing/queries.ts:107', path: 'v1/commission/overview', schema: commissionOverviewSchema },
  { at: 'marketing/queries.ts:116', path: 'v1/withdrawals', schema: withdrawalsResponse },

  // ---- 节点 ----
  { at: 'nodes/queries.ts:24', path: 'v1/nodes', query: { limit: 1000, include_retired: '1' }, schema: nodesResponse },
  { at: 'nodes/queries.ts:34', path: 'v1/node-protocol-schemas', schema: protocolSchemasResponse },
  { at: 'nodes/queries.ts:48', path: 'v1/servers', schema: serversResponse },
  // useServer 目前没有调用处（死 hook），接口仍验一遍
  { at: 'nodes/queries.ts:56', path: `v1/servers/${s.server_id}`, schema: serverSchema },
  { at: 'nodes/queries.ts:63', path: `v1/servers/${s.server_id}/nodes`, schema: serverNodesResponse },
  { at: 'nodes/queries.ts:72', path: 'v1/node-pools', schema: poolsResponse },
  { at: 'nodes/queries.ts:80', path: `v1/nodes/${s.node_id}/identity`, schema: identitySchema },
  { at: 'nodes/queries.ts:88', path: `v1/nodes/${s.node_id}/metrics`, query: { minutes: 1440 }, schema: metricsSchema },
  { at: 'nodes/queries.ts:95', path: `v1/nodes/${s.node_id}/routing`, schema: nodeRoutingSchema },
  { at: 'nodes/queries.ts:101', path: 'v1/nodes/routing', schema: globalRoutingSchema },

  // ---- 内容与外观 ----
  { at: 'content/queries.ts:21', path: 'v1/announcements', schema: announcementsResponse },
  { at: 'content/queries.ts:34', path: 'v1/content-pages', query: { kind: 'kb_article', limit: 500 }, schema: pagesResponse },
  { at: 'content/queries.ts:47', path: `v1/content-pages/${pageId}`, schema: pageResponse },
  { at: 'content/queries.ts:58', path: 'v1/plans', schema: planCatalogResponse },
  { at: 'content/queries.ts:69', path: 'v1/themes', schema: themesResponse },
  { at: 'content/queries.ts:75', path: 'v1/slots', schema: slotsResponse },
  { at: 'content/queries.ts:81', path: 'v1/settings/site', schema: siteSettingsSchema },

  // ---- 套餐 ----
  { at: 'plans/api.ts:24', path: 'v1/plans', schema: plansSchema },
  { at: 'plans/api.ts:33', path: `v1/plans/${s.plan_id}`, schema: planResponseSchema },
  { at: 'plans/api.ts:44', path: `v1/plans/${s.plan_id}/pools`, schema: planPoolsSchema },
  { at: 'plans/api.ts:55', path: 'v1/node-pools', schema: poolOptionsSchema },
  { at: 'plans/api.ts:65', path: 'v1/traffic-packs', schema: packsSchema },

  // ---- 系统 ----
  { at: 'system/queries.ts:18', path: 'v1/settings/mail', schema: mailSettingsSchema },
  { at: 'system/queries.ts:23', path: 'v1/settings/telegram', schema: telegramSettingsSchema },
  { at: 'system/queries.ts:28', path: 'v1/mail/templates', schema: mailTemplatesResponse },
  { at: 'system/queries.ts:33', path: 'v1/plugin-hooks', schema: hooksResponse },
  {
    at: 'system/queries.ts:41',
    path: 'v1/plugin-hooks/{code}/deliveries',
    schema: deliveriesResponse,
    skip: '种子里没有插件钩子（造一条要一个能收投递的地址），投递记录接口留给第 ④ 步写路径抽样',
  },

  // ---- 安全 ----
  { at: 'security/queries.ts:22', path: 'v1/audit', query: { limit: 50, offset: 0 }, schema: auditResponse },
  { at: 'security/queries.ts:33', path: 'v1/access-log', query: { limit: 50, offset: 0 }, schema: accessResponse },
  { at: 'security/queries.ts:43', path: 'v1/ip-clusters', schema: clustersResponse },
  { at: 'security/queries.ts:52', path: 'v1/switches', schema: switchesResponse },

  // ---- 财务 ----
  { at: 'billing/api.ts:32', path: 'v1/orders', query: { limit: 25, offset: 0 }, schema: ordersSchema },
  { at: 'billing/api.ts:42', path: `v1/orders/${s.order_id}`, schema: orderResponseSchema },
  { at: 'billing/api.ts:53', path: `v1/orders/${s.order_id}/payments`, schema: paymentHistorySchema },
  { at: 'billing/api.ts:65', path: 'v1/users', query: { q: 'smoke', limit: 8, offset: 0 }, schema: usersSchema },
  { at: 'billing/api.ts:76', path: 'v1/late-payments', query: { limit: 25, offset: 0 }, schema: latePaymentsSchema },
  { at: 'billing/api.ts:85', path: 'v1/payment-providers', schema: providersSchema },
  { at: 'billing/api.ts:94', path: 'v1/revenue/adjustments', schema: adjustmentsSchema },

  // ---- 非 JSON：两个 CSV 导出与事件流，只验状态与内容类型 ----
  { at: 'security/AuditTab.tsx:167', path: 'v1/audit/export', query: { from: '2026-01-01', to: '2099-12-31' }, kind: 'raw' },
  { at: 'users/BulkTab.tsx:135', path: 'v1/users/bulk/export', query: { status: 'active' }, kind: 'raw' },
  { at: 'shell/runtime.tsx:96', path: 'v1/events', kind: 'sse' },
]

// 列表里没取到 id 时行照跑（路径里是 missing）：404 本身就是要报告的事
runTable('admin', rows)
