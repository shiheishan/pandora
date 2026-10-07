import { randomUUID } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import { providersSchema, providerWrittenSchema } from '../../src/admin/screens/billing/schemas'
import { emptyProviderForm, providerBody, providerFormFrom } from '../../src/admin/screens/billing/model'
import { pageClient, record } from './harness'

// ============================================================================
//  支付渠道的新建与编辑（w2pay）：页面拼的请求体对真实网关走得通
// ============================================================================
// 新建的渠道是「已启用、暂停收新单」，结账页看不到，不影响两张读表与门户。
// 冒烟栈是开发模式（payctl 也靠它放行回环地址），站点地址用回环 + 允许内网，保存时不做 DNS。
// 文件名排在两张读表之后、writes.smoke.ts 之前（sequencer 只把 writes 固定在最后）。

function writeCase(at: string, path: string, title: string, fn: () => Promise<string | void>): void {
  it(`${title} ← ${at}`, async () => {
    try {
      const note = await fn()
      record('write', { at, path }, '已验', note ?? title)
    } catch (error) {
      record('write', { at, path }, '不一致', `${title}：${error instanceof Error ? error.message : String(error)}`)
      throw error
    }
  })
}

describe('支付渠道新建与编辑', () => {
  const code = `smoke-${randomUUID().slice(0, 8)}`

  writeCase('billing/ProviderDialog.tsx 新建', 'v1/payment-providers', '财务：新建易支付渠道（支付宝 + 微信）', async () => {
    const api = pageClient('admin')
    const form = { ...emptyProviderForm(), code, base_url: 'http://127.0.0.1:9', allow_private_host: true, merchant_id: 'smoke-pid', key: 'smoke-test-only-key' }
    const created = await api.post('v1/payment-providers', providerWrittenSchema, { body: providerBody(form, 'create'), idempotencyKey: randomUUID() })
    expect(created).toMatchObject({ code, credentials_changed: true })
    const row = (await api.get('v1/payment-providers', providersSchema)).providers.find((p) => p.code === code)
    expect(row, '新建的渠道不在列表里').toBeDefined()
    expect(row).toMatchObject({ enabled: true, accepting_new: false, has_credentials: true, methods: ['alipay', 'wxpay'], default_method: 'alipay' })
    return `渠道 ${code} 已建，暂停收新单`
  })

  writeCase('billing/ProviderDialog.tsx 编辑', 'v1/payment-providers/{code}', '财务：编辑渠道、凭据留空不改', async () => {
    const api = pageClient('admin')
    const row = (await api.get('v1/payment-providers', providersSchema)).providers.find((p) => p.code === code)
    expect(row, '上一步新建的渠道不在列表里').toBeDefined()
    const form = { ...providerFormFrom(row!), display_name: '冒烟易支付', methods: ['wxpay', 'qqpay'], default_method: 'wxpay' }
    const saved = await api.put(`v1/payment-providers/${encodeURIComponent(code)}`, providerWrittenSchema, { body: providerBody(form, 'edit'), idempotencyKey: randomUUID() })
    expect(saved.credentials_changed).toBe(false)
    const after = (await api.get('v1/payment-providers', providersSchema)).providers.find((p) => p.code === code)
    expect(after).toMatchObject({ display_name: '冒烟易支付', methods: ['wxpay', 'qqpay'], default_method: 'wxpay', has_credentials: true })
    return '改名与方式生效，凭据未变'
  })
})
