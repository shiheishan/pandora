import { describe, expect, it } from 'vitest'
import { CLIENT_FORMATS, extrasBody, extrasFormFrom, type SubscriptionExtras } from './subscription'

const saved: SubscriptionExtras = {
  info_nodes: false,
  protocol_prefix: true,
}

describe('extrasFormFrom', () => {
  it('keeps the two switches', () => {
    expect(extrasFormFrom(saved)).toEqual({ infoNodes: false, protocolPrefix: true })
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
