import { describe, expect, it } from 'vitest'
import { GiB, QUOTA_MAX_BYTES, isoToLocalInput, localToRFC3339, parseLimitGiB, periodEndProblem } from './subEdit'

const NOW = new Date('2026-06-15T12:00:00.000Z')

describe('subEdit', () => {
  it('localToRFC3339 / isoToLocalInput 往返（到分钟）', () => {
    const iso = '2026-12-01T08:00:00.000Z'
    const local = isoToLocalInput(iso)
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/)
    const back = localToRFC3339(local)
    expect(back).not.toBeNull()
    expect(new Date(back!).getTime()).toBe(new Date(isoToLocalInput(back!)).getTime())
    expect(localToRFC3339('')).toBeNull()
    expect(localToRFC3339('not-a-date')).toBeNull()
  })

  it('periodEndProblem：过去、超过 10 年、正常', () => {
    const msg = '到期时间须晚于现在，且不超过 10 年'
    expect(periodEndProblem('2020-01-01T00:00:00.000Z', NOW)).toBe(msg)
    expect(periodEndProblem(NOW.toISOString(), NOW)).toBe(msg)
    const eleven = new Date(NOW)
    eleven.setFullYear(eleven.getFullYear() + 11)
    expect(periodEndProblem(eleven.toISOString(), NOW)).toBe(msg)
    const ok = new Date(NOW.getTime() + 86_400_000).toISOString()
    expect(periodEndProblem(ok, NOW)).toBeNull()
  })

  it('parseLimitGiB', () => {
    expect(parseLimitGiB('')).toEqual({ bytes: null })
    expect(parseLimitGiB('100')).toEqual({ bytes: 100 * GiB })
    expect(parseLimitGiB('0.5')).toEqual({ bytes: Math.round(0.5 * GiB) })
    expect(parseLimitGiB('-1')).toMatchObject({ error: expect.any(String) })
    expect(parseLimitGiB('abc')).toMatchObject({ error: expect.any(String) })
    const over = QUOTA_MAX_BYTES / GiB + 1
    expect(parseLimitGiB(String(over))).toMatchObject({ error: expect.any(String) })
  })
})
