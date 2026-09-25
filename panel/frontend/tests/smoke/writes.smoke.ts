/**
 * [INPUT]: 依赖 ./harness 的 state / pageClient / loginToken / lastHeaders / record，依赖页面模块里写操作用的响应 schema 与请求体构造函数（annBody、salesBody、orderItems、toggleBody 等），依赖 node:crypto 的 HMAC（把真实会话的重认证时间往回拨）
 * [OUTPUT]: 第 ④ 步写路径冒烟：新服务器 + 新节点一步上线后能下发用户；reauth_required → reauth → 同键重放；幂等 2xx 同键重放与换请求体 409；每个后台模块与门户至少一个写操作的响应能被页面 schema 解析；工单回复发不发站内通知（FACT）
 * [POS]: tests/smoke 的写路径表，排在 admin / portal 两张读表之后跑（文件名序）；请求体一律用页面自己的构造函数拼，响应用页面自己的 schema 解析，结果以入口 write 进逐行表
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { createHmac, randomUUID } from 'node:crypto'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { z } from 'zod'
import { isApiError, type ApiClient } from '../../src/core/api'
import { toggledSchema } from '../../src/admin/screens/billing/schemas'
import { toggleBody } from '../../src/admin/screens/billing/model'
import { annBody, annFormFrom } from '../../src/admin/screens/content/logic'
import { annSaved, announcementsResponse } from '../../src/admin/screens/content/schemas'
import { codesResponse, batchesResponse, toggleResponse } from '../../src/admin/screens/marketing/schemas'
import { orderItems } from '../../src/admin/screens/nodes/logic'
import { nodesResponse, okUpdated } from '../../src/admin/screens/nodes/schemas'
import { salesBody, salesForm } from '../../src/admin/screens/plans/model'
import { planResponseSchema, rowVersionSchema } from '../../src/admin/screens/plans/schemas'
import { switchesResponse, switchSaved } from '../../src/admin/screens/security/schemas'
import { templatesResponse, templateSaved } from '../../src/admin/screens/system/schemas'
import { okSchema as ticketOk, ticketDetailSchema } from '../../src/admin/screens/tickets/api'
import { okSchema as usersOk } from '../../src/admin/screens/users/api'
import { linksSchema } from '../../src/portal/screens/common/subscriptions'
import { NOTIFICATION_LIMIT, notificationSchema } from '../../src/portal/screens/messages/api'
import { lastHeaders, loginToken, pageClient, record, state } from './harness'

const s = state.seed

/** 一条写路径用例：跑 fn，结果进逐行表（入口 write） */
function writeCase(at: string, path: string, title: string, fn: () => Promise<string | void>, timeout?: number): void {
  it(`${title} ← ${at}`, async () => {
    try {
      const note = await fn()
      record('write', { at, path }, '已验', note ?? title)
    } catch (error) {
      record('write', { at, path }, '不一致', `${title}：${error instanceof Error ? error.message : String(error)}`)
      throw error
    }
  }, timeout)
}

// ============================================================================
//  上线后下发：以节点身份取用户列表，门户用户的订阅凭据要在里面
// ============================================================================

describe('新服务器 + 新节点一步上线后能下发用户', () => {
  writeCase('seed.ts（接入 + POST activate）', 'api/v1/server/UniProxy/user', '节点用户列表含门户用户的订阅凭据', async () => {
    const res = await fetch(`${state.node}/api/v1/server/UniProxy/user?node_id=${s.node_id}&node_type=${s.node_type}`, {
      headers: { Authorization: `Bearer ${s.node_runtime_token}` },
    })
    expect(res.status).toBe(200)
    const body = (await res.json()) as { users?: Array<{ uuid: string }>; data?: { users?: Array<{ uuid: string }> } }
    const uuids = (body.users ?? body.data?.users ?? []).map((u) => u.uuid)
    expect(uuids.length).toBeGreaterThan(0)
    // 门户用户拿不到裸的 proxy_uuid；它随订阅链接下发的配置里就是 shadowsocks 的 password
    const links = await pageClient('portal').get('v1/me/subscription-links', linksSchema)
    const link = links.links.find((l) => l.subscription_id === s.subscription_id)
    expect(link, '门户订阅链接里没有种子订阅').toBeDefined()
    const config = await (await fetch(`${new URL(link!.url, state.portal)}${link!.url.includes('?') ? '&' : '?'}flag=sing-box`)).text()
    const hit = uuids.filter((u) => config.includes(u))
    expect(hit.length, '订阅配置里找不到节点用户列表里的任何凭据').toBeGreaterThan(0)
    return `节点列出 ${uuids.length} 个用户，门户用户的凭据在其中`
  })
})

// ============================================================================
//  重认证：同一个真实会话，把令牌里的 rat 拨回 16 分钟前（等价于 15 分钟窗口已过，免得 CI 空等），
//  由页面的 api 客户端自己走 403 reauth_required → requestReauth → POST auth/reauth → 同键重放
// ============================================================================

function staleAdminToken(): string {
  const [v, aud, payload] = loginToken('admin').split('.')
  const claims = JSON.parse(Buffer.from(payload!, 'base64url').toString('utf8')) as Record<string, unknown>
  claims.rat = Math.floor(Date.now() / 1000) - 16 * 60
  const body = `${v}.${aud}.${Buffer.from(JSON.stringify(claims)).toString('base64url')}`
  const sig = createHmac('sha256', Buffer.from(state.adminJwtSecret, 'base64')).update(body).digest('base64url')
  return `${body}.${sig}`
}

describe('reauth_required → reauth → 同键重放', () => {
  writeCase('content/AnnounceEditor.tsx:63', 'v1/announcements/{id}', '过期的重认证由页面客户端补上并原键重放', async () => {
    let asked = 0
    // 与后台外框常驻对话框同一个接口：用户输入口令后由对话框调 client.reauth
    const requestReauth = async () => {
      asked++
      await client.reauth(state.adminPassword)
      return true
    }
    // requestReauth 只在请求被拦下时才调，那时 client 早已建好
    const client: ApiClient = pageClient('admin', { token: staleAdminToken(), requestReauth })
    const list = await client.get('v1/announcements', announcementsResponse)
    const original = list.announcements.find((a) => a.id === s.announcement_id)
    expect(original, '公告列表里没有种子公告').toBeDefined()
    const form = { ...annFormFrom(original!), body: `${original!.body}（冒烟改）` }
    const saved = await client.post(`v1/announcements/${s.announcement_id}`, annSaved, { body: annBody(form, true, original!), idempotencyKey: randomUUID() })
    expect(asked, '服务端没有回 reauth_required，页面客户端没有走重认证').toBe(1)
    expect(saved.version).toBeGreaterThan(original!.version)
    return `403 reauth_required 一次 → reauth → 同键重放成功，公告版本 ${original!.version} → ${saved.version}`
  })
})

// ============================================================================
//  幂等：后台工单回复。同键同请求体重放回原响应（带 Idempotency-Replayed），换请求体 409；
//  消息只多一条
// ============================================================================

describe('幂等：2xx 同键重放与换请求体 409', () => {
  writeCase('tickets/Composer.tsx:52', 'v1/tickets/{id}/reply', '同键重放不重做，换请求体 409 idempotency_key_reuse', async () => {
    const api = pageClient('admin')
    const before = (await api.get(`v1/tickets/${s.ticket_id}`, ticketDetailSchema)).messages.length
    const key = randomUUID()
    const path = `v1/tickets/${s.ticket_id}/reply`
    const body = { body: '冒烟：幂等首发', internal_note: false }
    await api.post(path, ticketOk, { body, idempotencyKey: key })
    await api.post(path, ticketOk, { body, idempotencyKey: key })
    const replayed = [...lastHeaders.entries()].reverse().find(([u]) => u.includes(`/${path}`))?.[1].get('Idempotency-Replayed')
    expect(replayed, '同键重放没有带 Idempotency-Replayed').toBe('true')
    const conflict = await api.post(path, ticketOk, { body: { ...body, body: '冒烟：换了请求体' }, idempotencyKey: key }).then(
      () => null,
      (e: unknown) => e,
    )
    expect(isApiError(conflict) && conflict.status === 409 && conflict.code === 'idempotency_key_reuse', `换请求体没有回 409 idempotency_key_reuse：${String(conflict)}`).toBe(true)
    const after = (await api.get(`v1/tickets/${s.ticket_id}`, ticketDetailSchema)).messages.length
    expect(after - before, '重放又写了一条消息').toBe(1)
    return '首发 200、同键重放 200 且 Idempotency-Replayed: true、换体 409 idempotency_key_reuse，消息只多一条'
  })
})

// ============================================================================
//  每个模块至少一个写操作：请求体用页面的构造函数，响应用页面的 schema。
//  仪表盘模块只读，没有写操作；内容模块由上面的重认证用例覆盖，工单由幂等用例覆盖
// ============================================================================

describe('各模块写操作的响应能被页面 schema 解析', () => {
  writeCase('users/tabs.tsx:186', 'v1/subscriptions/{id}/device-limit', '用户：改设备上限', async () => {
    await pageClient('admin').post(`v1/subscriptions/${s.subscription_id}/device-limit`, usersOk, { body: { limit: 5 } })
  })

  writeCase('marketing/Gifts.tsx:237', 'v1/gift-cards/codes/{id}/toggle', '营销：停用再恢复一张礼品卡码', async () => {
    const api = pageClient('admin')
    const batches = await api.get('v1/gift-cards/batches', batchesResponse, { query: { limit: 50, offset: 0 } })
    const codes = await api.get('v1/gift-cards/codes', codesResponse, { query: { batch_id: batches.items[0]!.id, limit: 50, offset: 0 } })
    const unused = codes.codes.find((c) => c.status === 'unused')
    expect(unused, '批次里没有未使用的码').toBeDefined()
    expect((await api.post(`v1/gift-cards/codes/${unused!.id}/toggle`, toggleResponse, { body: { disabled: true } })).disabled).toBe(true)
    expect((await api.post(`v1/gift-cards/codes/${unused!.id}/toggle`, toggleResponse, { body: { disabled: false } })).disabled).toBe(false)
  })

  writeCase('nodes/NodesTab.tsx:69', 'v1/nodes/order', '节点：保存排序', async () => {
    const api = pageClient('admin')
    const nodes = await api.get('v1/nodes', nodesResponse, { query: { limit: 1000, include_retired: '1' } })
    await api.put('v1/nodes/order', okUpdated, { body: { items: orderItems(nodes.nodes) } })
  })

  writeCase('plans/SalesDrawer.tsx:48', 'v1/plans/{id}', '套餐：原样保存销售设置', async () => {
    const api = pageClient('admin')
    const { plan } = await api.get(`v1/plans/${s.plan_id}`, planResponseSchema)
    const body = salesBody(salesForm(plan), plan)
    await api.put(`v1/plans/${s.plan_id}`, rowVersionSchema, { body, idempotencyKey: randomUUID() })
  })

  writeCase('system/TemplatesTab.tsx:126', 'v1/mail/templates', '系统：原样保存一个通知模板', async () => {
    const api = pageClient('admin')
    const t = (await api.get('v1/mail/templates', templatesResponse)).templates[0]
    expect(t, '没有任何通知模板').toBeDefined()
    await api.post('v1/mail/templates', templateSaved, { body: { code: t!.code, channel: t!.channel, subject: t!.subject, body: t!.body } })
  })

  writeCase('security/SwitchesTab.tsx:90', 'v1/switches/{code}', '安全：关闭再打开礼品卡兑换开关', async () => {
    const api = pageClient('admin')
    const sw = (await api.get('v1/switches', switchesResponse)).switches.find((x) => x.code === 'marketing.giftcard.redeem')
    expect(sw, '没有 marketing.giftcard.redeem 开关').toBeDefined()
    expect((await api.post('v1/switches/marketing.giftcard.redeem', switchSaved, { body: { enabled: false, reason: '冒烟：写路径校验' } })).enabled).toBe(false)
    expect((await api.post('v1/switches/marketing.giftcard.redeem', switchSaved, { body: { enabled: true, reason: '冒烟：写路径校验恢复' } })).enabled).toBe(true)
  })

  writeCase('billing/ProvidersTab.tsx:116', 'v1/payment-providers/{code}/toggle', '财务：演示渠道设为收新单', async () => {
    await pageClient('admin').post('v1/payment-providers/demo/toggle', toggledSchema, { body: toggleBody('on') })
  })

  writeCase('portal tickets/api.ts:135', 'v1/support/tickets/{id}/reply', '门户：回复自己的工单', async () => {
    await pageClient('portal').post(`v1/support/tickets/${s.ticket_id}/reply`, z.object({ ok: z.literal(true) }), {
      body: { body: '冒烟：门户补充一句' },
      idempotencyKey: randomUUID(),
    })
  })
})

// ============================================================================
//  插件投递：本机接收端真的收到了 ticket.created（不只是投递表里有一行 sent）
// ============================================================================

describe('插件钩子投递到本机接收端', () => {
  writeCase('system/queries.ts:41', 'v1/plugin-hooks/{code}/deliveries', '接收端收到带签名头的 ticket.created', async () => {
    const file = join(state.dir, 'hook-received.jsonl')
    // 扫描器 20 秒后第一次、之后每 60 秒；读表那行已经等到 sent，这里再给 90 秒兜底
    const deadline = Date.now() + 90_000
    while (!existsSync(file) && Date.now() < deadline) await new Promise((r) => setTimeout(r, 5000))
    const got = existsSync(file) ? readFileSync(file, 'utf8').trim().split('\n').map((l) => JSON.parse(l) as { event: string; signed: boolean }) : []
    const hit = got.find((g) => g.event === 'ticket.created')
    expect(hit, '接收端没有收到 ticket.created').toBeDefined()
    expect(hit!.signed, '投递缺 X-Pandora-Signature / X-Pandora-Timestamp').toBe(true)
    return `接收端收到 ${got.length} 次投递，含带签名头的 ticket.created`
  }, 120_000)
})

// ============================================================================
//  工单回复发不发站内通知（FACT）：上面已有后台回复；门户通知列表里不应出现 ticket.replied
//  （代码：support.replyAsAgent 不调 notify.Enqueue，只推一条实时 ticket.updated）
// ============================================================================

describe('工单回复与站内通知', () => {
  writeCase('messages/api.ts:31', 'v1/me/notifications', '后台回复工单后门户通知列表里有没有 ticket.replied', async () => {
    const list = await pageClient('portal').get('v1/me/notifications', z.object({ notifications: z.array(notificationSchema), unread: z.number().int() }), {
      query: { limit: NOTIFICATION_LIMIT },
    })
    const codes = list.notifications.map((n) => n.code)
    expect(codes).not.toContain('ticket.replied')
    return `FACT：后台回复后门户通知只有 [${[...new Set(codes)].join(', ') || '无'}]，没有 ticket.replied`
  })
})
