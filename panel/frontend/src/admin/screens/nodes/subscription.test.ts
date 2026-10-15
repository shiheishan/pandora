import { describe, expect, it } from 'vitest'
import {
  extrasBody,
  extrasFormFrom,
  formatLabel,
  formatSize,
  missingPlaceholders,
  templateBytes,
  type SubscriptionExtras,
} from './subscription'

const saved: SubscriptionExtras = {
  info_nodes: false,
  protocol_prefix: true,
}

describe('formatLabel', () => {
  it('returns label for known format', () => {
    expect(formatLabel('clash')).toBe('Clash Meta（mihomo）')
  })
  it('returns format as-is when unknown', () => {
    expect(formatLabel('unknown')).toBe('unknown')
  })
})

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

describe('templateBytes', () => {
  it('counts UTF-8 bytes', () => {
    expect(templateBytes('中')).toBe(3)
  })
})

describe('missingPlaceholders', () => {
  it('lists missing required tokens', () => {
    expect(
      missingPlaceholders('hello', [
        { token: '{{nodes}}', required: true, desc: '' },
        { token: '{{opt}}', required: false, desc: '' },
      ]),
    ).toEqual(['{{nodes}}'])
  })

  it('returns empty when all required present', () => {
    expect(missingPlaceholders('{{nodes}}', [{ token: '{{nodes}}', required: true, desc: '' }])).toEqual([])
  })
})

describe('formatSize', () => {
  it('formats bytes and kilobytes', () => {
    expect(formatSize(512)).toBe('512 B')
    expect(formatSize(2048)).toBe('2.0 KB')
  })
})
