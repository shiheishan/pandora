/**
 * [INPUT]: 依赖 vitest，依赖 ./api 的 ApiError，依赖 ./intent 的 createIntentKey / endsIntent
 * [OUTPUT]: 对外提供幂等键约定的单元测试
 * [POS]: core/intent.ts 的测试（第 4 阶段 ④ 从 admin.test.ts 与门户 common.test.ts 收拢过来）：同指纹同键、改了换键、reset 后必换、默认 UUID v4；endsIntent 的四种情形（4xx 结束、reauth 取消保留、断网与 5xx 保留、非 ApiError 保留）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { createIntentKey, endsIntent } from './intent'

describe('createIntentKey', () => {
  it('同一意图复用一把键，意图变了换新键，reset 后必换', () => {
    let n = 0
    const intent = createIntentKey(() => `k${++n}`)
    const first = intent.keyFor(['t1', { body: 'hi' }])
    expect(intent.keyFor(['t1', { body: 'hi' }])).toBe(first)
    const changed = intent.keyFor(['t1', { body: 'hi!' }])
    expect(changed).not.toBe(first)
    expect(intent.keyFor(['t1', { body: 'hi!' }])).toBe(changed)
    intent.reset()
    expect(intent.keyFor(['t1', { body: 'hi!' }])).not.toBe(changed)
    expect(n).toBe(3)
  })

  it('默认生成 UUID v4', () => {
    expect(createIntentKey().keyFor('x')).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
  })
})

describe('endsIntent（契约 1.5 与 R85：后端只重放 2xx）', () => {
  const failure = (status: number, code: ConstructorParameters<typeof ApiError>[0]['code']) => new ApiError({ status, code, message: `失败 ${status}` })

  it('4xx 业务拒绝结束意图', () => {
    for (const [status, code] of [
      [400, 'bad_request'],
      [404, 'not_found'],
      [409, 'conflict'],
      [409, 'idempotency_key_reuse'],
      [422, 'validation_failed'],
      [429, 'rate_limited'],
    ] as const)
      expect(endsIntent(failure(status, code)), `${status} ${code}`).toBe(true)
  })

  it('reauth 取消（R34）处理器没执行、键没消耗，保留', () => {
    expect(endsIntent(failure(403, 'reauth_required'))).toBe(false)
  })

  it('断网、5xx 与 2xx 回包解析失败保留键，重试时回放', () => {
    expect(endsIntent(failure(0, 'network_error'))).toBe(false)
    expect(endsIntent(failure(500, 'internal_error'))).toBe(false)
    expect(endsIntent(failure(503, 'service_unavailable'))).toBe(false)
    expect(endsIntent(failure(200, 'invalid_response'))).toBe(false)
  })

  it('非 ApiError 保留', () => {
    expect(endsIntent(new Error('boom'))).toBe(false)
  })
})
