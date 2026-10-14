import { describe, expect, it } from 'vitest'
import {
  extrasBody,
  extrasFormFrom,
  formatLabel,
  formatSize,
  missingPlaceholders,
  parseUrlLines,
  templateBytes,
  type SubscriptionExtras,
} from './subscription'

const saved: SubscriptionExtras = {
  info_nodes: false,
  protocol_prefix: true,
  link_base_urls: ['https://sub1.example.com'],
}

describe('formatLabel', () => {
  it('returns label for known format', () => {
    expect(formatLabel('clash')).toBe('Clash Meta（mihomo）')
  })
  it('returns format as-is when unknown', () => {
    expect(formatLabel('unknown')).toBe('unknown')
  })
})

describe('parseUrlLines', () => {
  it('trims lines and drops empty', () => {
    expect(parseUrlLines(' a \r\n\n b ')).toEqual(['a', 'b'])
  })
})

describe('extrasFormFrom', () => {
  it('joins urls with newline', () => {
    expect(extrasFormFrom({ ...saved, link_base_urls: ['https://a.example.com', 'https://b.example.com'] }).urlsText).toBe(
      'https://a.example.com\nhttps://b.example.com',
    )
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

  it('errors when more than 10 urls', () => {
    const lines = Array.from({ length: 11 }, (_, i) => `https://sub${i}.example.com`).join('\n')
    const form = { ...extrasFormFrom(saved), urlsText: lines }
    expect(extrasBody(form, saved)).toEqual({ errors: { link_base_urls: '订阅地址最多 10 个' } })
  })

  it('errors when a line does not start with https://', () => {
    const form = { ...extrasFormFrom(saved), urlsText: 'https://ok.example.com\nhttp://bad.example.com' }
    expect(extrasBody(form, saved)).toEqual({ errors: { link_base_urls: '第 2 个订阅地址必须以 https:// 开头' } })
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
