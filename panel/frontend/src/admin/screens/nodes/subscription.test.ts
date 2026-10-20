import { describe, expect, it } from 'vitest'
import { CLIENT_FORMATS, extrasBody, extrasFormFrom, fetchLimitError, type SubscriptionExtras } from './subscription'

const saved: SubscriptionExtras = {
  info_nodes: false,
  protocol_prefix: true,
  fetch_limit_per_hour: 60,
}

describe('extrasFormFrom', () => {
  it('keeps the two switches and the fetch limit', () => {
    expect(extrasFormFrom(saved)).toEqual({ infoNodes: false, protocolPrefix: true, fetchLimit: '60' })
  })
})

describe('extrasBody', () => {
  it('only includes changed fields', () => {
    const form = extrasFormFrom(saved)
    const out = extrasBody({ ...form, infoNodes: true }, saved)
    expect(out).toEqual({ body: { info_nodes: true } })
  })

  it('returns empty body when nothing changed', () => {
    const form = extrasFormFrom(saved)
    expect(extrasBody(form, saved)).toEqual({ body: {} })
  })

  it('submits a changed fetch limit as a number', () => {
    const form = extrasFormFrom(saved)
    expect(extrasBody({ ...form, fetchLimit: '120' }, saved)).toEqual({ body: { fetch_limit_per_hour: 120 } })
  })

  it('drops an invalid fetch limit from the body', () => {
    const form = extrasFormFrom(saved)
    expect(extrasBody({ ...form, fetchLimit: 'abc' }, saved)).toEqual({ body: {} })
  })
})

describe('fetchLimitError', () => {
  it('rejects out-of-range and non-integer input', () => {
    for (const text of ['0', '10001', '1.5', '', 'abc']) {
      expect(fetchLimitError(text)).toBe('每小时拉取上限要在 1 到 10000 之间')
    }
  })

  it('accepts integers within range, ignoring surrounding spaces', () => {
    for (const text of ['1', ' 60 ', '10000']) {
      expect(fetchLimitError(text)).toBeNull()
    }
  })
})

describe('CLIENT_FORMATS', () => {
  it('has exactly one fallback row as the last entry', () => {
    const fallbacks = CLIENT_FORMATS.filter((r) => r.fallback)
    expect(fallbacks).toHaveLength(1)
    expect(CLIENT_FORMATS[CLIENT_FORMATS.length - 1]?.fallback).toBe(true)
  })

  it('does not mention surge in any column', () => {
    for (const row of CLIENT_FORMATS) {
      expect(row.clients.toLowerCase()).not.toContain('surge')
      expect(row.ua.toLowerCase()).not.toContain('surge')
      expect(row.format.toLowerCase()).not.toContain('surge')
    }
  })
})
