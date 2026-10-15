import { describe, expect, it } from 'vitest'
import {
  isProbeDone,
  markProbed,
  probeFrameURL,
  PROBE_DONE_TYPE,
  PROBE_INTERVAL_MS,
  PROBE_STORAGE_KEY,
  shouldProbe,
} from './probe'

describe('shouldProbe & markProbed', () => {
  it('storage 为 null 时 shouldProbe 为 true', () => {
    expect(shouldProbe(Date.now(), null)).toBe(true)
  })

  it('没记录时 shouldProbe 为 true', () => {
    const store = new Map<string, string>()
    const storage = { getItem: (k: string) => store.get(k) ?? null }
    expect(shouldProbe(Date.now(), storage)).toBe(true)
  })

  it('刚记录时 shouldProbe 为 false', () => {
    const store = new Map<string, string>()
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
    }
    const now = 1_000_000
    markProbed(now, storage)
    expect(store.get(PROBE_STORAGE_KEY)).toBe('1000000')
    expect(shouldProbe(now + 10_000, storage)).toBe(false)
  })

  it('超过 30 分钟时 shouldProbe 为 true', () => {
    const store = new Map<string, string>()
    const storage = {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => store.set(k, v),
    }
    const now = 1_000_000
    markProbed(now, storage)
    expect(shouldProbe(now + PROBE_INTERVAL_MS, storage)).toBe(true)
    expect(shouldProbe(now + PROBE_INTERVAL_MS + 1000, storage)).toBe(true)
  })

  it('损坏值时 shouldProbe 为 true', () => {
    const invalidValueStorage = {
      getItem: () => 'not-a-number',
    }
    expect(shouldProbe(Date.now(), invalidValueStorage)).toBe(true)
  })

  it('getItem 抛错时 shouldProbe 为 true', () => {
    const faultyStorage = {
      getItem: () => {
        throw new Error('Access denied')
      },
    }
    expect(shouldProbe(Date.now(), faultyStorage)).toBe(true)
  })

  it('markProbed 在 setItem 抛错时静默不抛', () => {
    const faultyStorage = {
      setItem: () => {
        throw new Error('QuotaExceeded')
      },
    }
    expect(() => markProbed(Date.now(), faultyStorage)).not.toThrow()
  })

  it('markProbed 在 storage 为 null 时不抛出', () => {
    expect(() => markProbed(Date.now(), null)).not.toThrow()
  })
})

describe('probeFrameURL', () => {
  it('相对入口目录解析探测页地址', () => {
    expect(probeFrameURL('a.b-c_d', 'https://portal.example.test/')).toBe(
      'https://portal.example.test/v1/sub-probe/frame?t=a.b-c_d',
    )
  })

  it('hash 路由入口下结果相同', () => {
    expect(probeFrameURL('a.b-c_d', 'https://portal.example.test/#/subs')).toBe(
      'https://portal.example.test/v1/sub-probe/frame?t=a.b-c_d',
    )
  })

  it('票据含 + / = 时查询参数正确编码', () => {
    const ticket = 'a+b/c=d'
    const url = probeFrameURL(ticket, 'https://portal.example.test/')
    expect(new URL(url).searchParams.get('t')).toBe(ticket)
  })
})

describe('isProbeDone', () => {
  const origin = 'https://portal.example.test'
  const frameWindow = { kind: 'frame' }

  it('同源 + 同一 source + 正确 type 为 true', () => {
    expect(
      isProbeDone(
        { origin, source: frameWindow, data: { type: PROBE_DONE_TYPE } },
        origin,
        frameWindow,
      ),
    ).toBe(true)
  })

  it('origin 不同为 false', () => {
    expect(
      isProbeDone(
        { origin: 'https://evil.example.test', source: frameWindow, data: { type: PROBE_DONE_TYPE } },
        origin,
        frameWindow,
      ),
    ).toBe(false)
  })

  it('source 不同为 false', () => {
    expect(
      isProbeDone(
        { origin, source: { kind: 'other' }, data: { type: PROBE_DONE_TYPE } },
        origin,
        frameWindow,
      ),
    ).toBe(false)
  })

  it('frameWindow 为 null 为 false', () => {
    expect(
      isProbeDone({ origin, source: frameWindow, data: { type: PROBE_DONE_TYPE } }, origin, null),
    ).toBe(false)
  })

  it('data 为字符串为 false', () => {
    expect(isProbeDone({ origin, source: frameWindow, data: 'x' }, origin, frameWindow)).toBe(false)
  })

  it('type 不同为 false', () => {
    expect(
      isProbeDone({ origin, source: frameWindow, data: { type: 'other' } }, origin, frameWindow),
    ).toBe(false)
  })
})
