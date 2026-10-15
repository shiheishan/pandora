import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bulkBanSchema } from '../src/admin/screens/users/opsSchemas'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin bulk ban', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    auth = bearer((await loginAs(base, MOCK_ACCOUNTS.admin)).access_token)
  })
  afterAll(() => close(server))

  const send = (method: string, path: string, body?: unknown, key?: string) => mockFetch(base, auth, method, path, body, key)

  it('rejects without Idempotency-Key or with bad parameters', async () => {
    // 缺 Idempotency-Key
    const noKey = await send('POST', '/v1/users/bulk/ban', { status: 'suspended', expected_total: 1, reason: '违规批量封禁测试' })
    expect(noKey.status).toBe(400)

    // 空条件 422 fields.filter
    const emptyFilter = await send(
      'POST',
      '/v1/users/bulk/ban',
      { expected_total: 1, reason: '违规批量封禁测试' },
      'ban-empty-filter',
    )
    expect(emptyFilter.status).toBe(422)
    const emptyJson = (await emptyFilter.json()) as { error: { fields: Record<string, string> } }
    expect(emptyJson.error.fields.filter).toBe('批量封禁至少要选一个筛选条件')

    // 原因太短 422 fields.reason
    const shortReason = await send(
      'POST',
      '/v1/users/bulk/ban',
      { status: 'suspended', expected_total: 1, reason: '短' },
      'ban-short-reason',
    )
    expect(shortReason.status).toBe(422)
    const shortJson = (await shortReason.json()) as { error: { fields: Record<string, string> } }
    expect(shortJson.error.fields.reason).toBe('请写清原因，5 到 500 个字')
  })

  it('rejects with 409 if expected_total does not match matcher hits', async () => {
    // 预览获取真实人数
    const previewRes = await send('POST', '/v1/users/bulk/preview', { status: 'suspended' })
    expect(previewRes.status).toBe(200)
    const preview = (await previewRes.json()) as { total: number }

    // 故意传错误的 expected_total
    const mismatch = await send(
      'POST',
      '/v1/users/bulk/ban',
      { status: 'suspended', expected_total: preview.total + 99, reason: '违规批量封禁测试' },
      'ban-mismatch',
    )
    expect(mismatch.status).toBe(409)
    const errJson = (await mismatch.json()) as { error: { message: string } }
    expect(errJson.error.message).toContain('命中人数变了')
  })

  it('bans successfully, reports skipped accurately, and hits become 0 or reflect banned status', async () => {
    // 1. 预览
    const filter = { status: 'suspended' }
    const previewRes = await send('POST', '/v1/users/bulk/preview', filter)
    const preview = (await previewRes.json()) as { total: number }
    expect(preview.total).toBeGreaterThan(0)

    // 2. 执行批量封禁
    const banRes = await send(
      'POST',
      '/v1/users/bulk/ban',
      { ...filter, expected_total: preview.total, reason: '批量封禁违规暂停账号' },
      'ban-success-1',
    )
    expect(banRes.status).toBe(200)
    const banData = bulkBanSchema.parse(await banRes.json())

    const skippedTotal = Object.values(banData.skipped).reduce((acc, count) => acc + count, 0)
    expect(banData.banned + skippedTotal).toBe(preview.total)

    // 3. 幂等重放
    const replay = await send(
      'POST',
      '/v1/users/bulk/ban',
      { ...filter, expected_total: preview.total, reason: '批量封禁违规暂停账号' },
      'ban-success-1',
    )
    expect(replay.status).toBe(200)
    expect(await replay.json()).toEqual(banData)

    // 4. 再次按原条件预览，suspended 账号全变成了 banned，total 应为 0
    const previewAfter = await send('POST', '/v1/users/bulk/preview', filter)
    const afterData = (await previewAfter.json()) as { total: number }
    expect(afterData.total).toBe(0)
  })
})
