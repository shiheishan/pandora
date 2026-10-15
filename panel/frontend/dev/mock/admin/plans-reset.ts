import type { Json, MockModule, MockResult } from '../types.ts'
import { find, plans } from './plans-store.ts'

interface ResetPrice {
  currency: 'CNY' | 'USD'
  unit_amount: number
}

const err = (status: number, code: string, message: string, fields?: Record<string, string>): MockResult => ({
  status,
  body: { error: { code, message, ...(fields ? { fields } : {}) } },
})
const invalid = (fields: Record<string, string>) => err(422, 'validation_failed', '请求参数校验未通过', fields)
const NOT_FOUND = err(404, 'not_found', '资源不存在或无权访问')

const store = new Map<string, ResetPrice[]>()

// 种子：给第一个在售套餐配 CNY 1800
const firstActive = plans.find((p) => p.status === 'active')
if (firstActive) {
  store.set(firstActive.id, [{ currency: 'CNY', unit_amount: 1800 }])
}

export const planResets: MockModule = {
  routes: {
    'GET /v1/plans/:id/reset-prices': (ctx) => {
      const p = find(ctx.params.id ?? '')
      if (!p) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      const list = store.get(p.id) ?? []
      const sorted = [...list].sort((a, b) => a.currency.localeCompare(b.currency))
      ctx.send(200, { reset_prices: sorted })
    },

    'PUT /v1/plans/:id/reset-prices': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')

      await ctx.idempotent('catalog_plan_reset_prices', () => {
        // 拒绝顶层多余字段
        const topExtra = Object.keys(body).find((k) => k !== 'reset_prices')
        if (topExtra) return err(400, 'bad_request', `请求体包含未知字段 "${topExtra}"`)

        if (!Array.isArray(body.reset_prices)) {
          return invalid({ reset_prices: '重置价格列表格式不正确' })
        }

        const rawList = body.reset_prices as Json[]
        if (rawList.length > 2) {
          return invalid({ reset_prices: '最多配置 CNY、USD 各一档' })
        }

        const fields: Record<string, string> = {}
        const seen = new Set<string>()
        const validated: ResetPrice[] = []

        for (let i = 0; i < rawList.length; i++) {
          const item = rawList[i]
          if (!item || typeof item !== 'object') {
            fields[`reset_prices.${i}`] = '格式不正确'
            continue
          }
          const itemExtra = Object.keys(item).find((k) => k !== 'currency' && k !== 'unit_amount')
          if (itemExtra) {
            return err(400, 'bad_request', `请求体包含未知字段 "${itemExtra}"`)
          }

          const currency = typeof item.currency === 'string' ? item.currency : ''
          if (currency !== 'CNY' && currency !== 'USD') {
            fields[`reset_prices.${i}.currency`] = '只支持 CNY 或 USD'
          } else if (seen.has(currency)) {
            fields[`reset_prices.${i}.currency`] = '同一币种只能填一档'
          } else {
            seen.add(currency)
          }

          const amount = item.unit_amount
          if (typeof amount !== 'number' || !Number.isInteger(amount) || amount <= 0) {
            fields[`reset_prices.${i}.unit_amount`] = '重置价格须大于 0，最多两位小数'
          }

          if (Object.keys(fields).length === 0) {
            validated.push({ currency: currency as 'CNY' | 'USD', unit_amount: amount as number })
          }
        }

        if (Object.keys(fields).length > 0) {
          return invalid(fields)
        }

        const p = find(ctx.params.id ?? '')
        if (!p) return NOT_FOUND

        validated.sort((a, b) => a.currency.localeCompare(b.currency))
        store.set(p.id, validated)

        return { status: 200, body: { reset_prices: validated } }
      })
    },
  },
}
