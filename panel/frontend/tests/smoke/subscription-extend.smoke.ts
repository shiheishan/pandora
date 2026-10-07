import { randomUUID } from 'node:crypto'
import { describe, expect, it } from 'vitest'
import type { ApiClient } from '../../src/core/api'
import { extendedSchema, userDetailSchema } from '../../src/admin/screens/users/api'
import { linksSchema } from '../../src/portal/screens/common/subscriptions'
import { lastHeaders, pageClient, record, state } from './harness'

// ============================================================================
//  后台加时长（第 2 波 w2sub）：对真实网关走一遍 权限 → 重认证窗口 → 幂等 → 同事务完成幂等记录。
//  按文件名排在两张读表之后、writes.smoke.ts 之前；种子订阅延期后仍是 active，后面的写路径照常
// ============================================================================

const s = state.seed
const at = 'users/ExtendDialog.tsx ExtendDialog'
const path = `v1/subscriptions/${s.subscription_id}/extend`
const DAY = 86_400_000

describe('后台加时长', () => {
  it(`加 7 天：订阅与门户链接的到期一起往后推，同键重放回同一份 ← ${at}`, async () => {
    try {
      // 两张读表跑得久时登录自带的 15 分钟重认证窗口可能已过：与后台外框的常驻对话框一样补上再原键重放
      const requestReauth = async () => {
        await admin.reauth(state.adminPassword)
        return true
      }
      const admin: ApiClient = pageClient('admin', { requestReauth })
      const before = await admin.get(`v1/users/${s.portal.user_id}`, userDetailSchema)
      const sub = before.subscriptions.find((x) => x.id === s.subscription_id)
      expect(sub?.status, '种子订阅不是生效中').toBe('active')
      expect(sub?.current_period_end, '种子订阅没有到期时间').toBeTruthy()

      const body = { days: 7, reason: '冒烟：补偿线路故障' }
      const key = `smoke-extend-${randomUUID()}`
      const first = await admin.post(path, extendedSchema, { body, idempotencyKey: key })
      expect(first.subscription_id).toBe(s.subscription_id)
      expect(Date.parse(first.previous_end)).toBe(Date.parse(sub!.current_period_end!))
      const want = Math.max(Date.parse(first.previous_end), Date.now()) + 7 * DAY
      expect(Math.abs(Date.parse(first.period_end) - want), '新到期不是「原到期与现在里更晚的那个」+ 7 天').toBeLessThan(5 * 60_000)

      // 同键同体重放：回同一份结果、带 Idempotency-Replayed，不再多加 7 天
      const replay = await admin.post(path, extendedSchema, { body, idempotencyKey: key })
      expect(replay).toEqual(first)
      const replayed = [...lastHeaders.entries()].reverse().find(([u]) => u.includes(`/${path}`))?.[1].get('Idempotency-Replayed')
      expect(replayed, '同键重放没有带 Idempotency-Replayed').toBe('true')

      const after = await admin.get(`v1/users/${s.portal.user_id}`, userDetailSchema)
      const end = after.subscriptions.find((x) => x.id === s.subscription_id)?.current_period_end
      expect(Date.parse(end ?? ''), '用户详情里的到期没变').toBe(Date.parse(first.period_end))
      // 门户链接的有效期就是凭据到期：与订阅周期末一致（修前礼品卡延期只改订阅一行）
      const links = await pageClient('portal').get('v1/me/subscription-links', linksSchema)
      const link = links.links.find((l) => l.subscription_id === s.subscription_id)
      expect(Date.parse(link?.expires_at ?? ''), '门户链接的有效期没有跟着延长').toBe(Date.parse(first.period_end))

      record('write', { at, path }, '已验', `到期 ${first.previous_end} → ${first.period_end}，同键重放回同一份，门户链接有效期一致`)
    } catch (error) {
      record('write', { at, path }, '不一致', `后台加时长：${error instanceof Error ? error.message : String(error)}`)
      throw error
    }
  })
})
