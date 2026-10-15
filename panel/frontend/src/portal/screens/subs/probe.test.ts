import { describe, expect, it, vi } from 'vitest'
import {
  markProbed,
  probeAll,
  probeDomain,
  shouldProbe,
  PROBE_INTERVAL_MS,
  PROBE_STORAGE_KEY,
} from './probe'

describe('probeDomain', () => {
  const base = 'https://sub-a.example.test'

  it('204 回 ok 并断言 fetch 参数与 URL', async () => {
    let capturedUrl = ''
    let capturedInit: RequestInit | undefined

    const fakeFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      capturedUrl = String(input)
      capturedInit = init
      return new Response(null, { status: 204 })
    })

    const outcome = await probeDomain(base, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 5000,
      wasBlocked: () => false,
    })

    expect(outcome).toBe('ok')
    expect(capturedUrl).toBe('https://sub-a.example.test/v1/sub-probe')
    expect(capturedInit).toMatchObject({
      mode: 'cors',
      credentials: 'omit',
      cache: 'no-store',
      referrerPolicy: 'no-referrer',
    })
    expect(capturedInit?.signal).toBeInstanceOf(AbortSignal)
  })

  it('500 回 fail', async () => {
    const fakeFetch = vi.fn(async () => new Response(null, { status: 500 }))
    const outcome = await probeDomain(base, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 5000,
      wasBlocked: () => false,
    })
    expect(outcome).toBe('fail')
  })

  it('fetch 抛错且 wasBlocked 为假时回 fail', async () => {
    const fakeFetch = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    })
    const outcome = await probeDomain(base, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 5000,
      wasBlocked: () => false,
    })
    expect(outcome).toBe('fail')
  })

  it('fetch 抛错且 wasBlocked 为真时回 blocked', async () => {
    const fakeFetch = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    })
    const outcome = await probeDomain(base, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 5000,
      wasBlocked: (b) => b === base,
    })
    expect(outcome).toBe('blocked')
  })

  it('超时导致 signal abort 时回 fail', async () => {
    const fakeFetch = vi.fn(
      (_input: RequestInfo | URL, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => {
            reject(new DOMException('The operation was aborted.', 'AbortError'))
          })
        }),
    )

    const outcome = await probeDomain(base, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 20,
      wasBlocked: () => false,
    })
    expect(outcome).toBe('fail')
  })
})

describe('probeAll', () => {
  it('丢掉 blocked 并保持输入顺序', async () => {
    const bases = [
      'https://sub-a.example.test',
      'https://sub-b.example.test',
      'https://sub-c.example.test',
      'https://sub-d.example.test',
    ]

    const fakeFetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('https://sub-a.example.test')) return new Response(null, { status: 204 })
      if (url.startsWith('https://sub-b.example.test')) throw new TypeError('CSP blocked')
      if (url.startsWith('https://sub-c.example.test')) return new Response(null, { status: 502 })
      return new Response(null, { status: 204 })
    })

    const results = await probeAll(bases, {
      fetch: fakeFetch as unknown as typeof fetch,
      timeoutMs: 1000,
      wasBlocked: (b) => b === 'https://sub-b.example.test',
    })

    expect(results).toEqual([
      { base_url: 'https://sub-a.example.test', ok: true },
      { base_url: 'https://sub-c.example.test', ok: false },
      { base_url: 'https://sub-d.example.test', ok: true },
    ])
  })
})

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

  it('getItem 抛错或值为非数字时 shouldProbe 为 true', () => {
    const faultyStorage = {
      getItem: () => {
        throw new Error('Access denied')
      },
    }
    expect(shouldProbe(Date.now(), faultyStorage)).toBe(true)

    const invalidValueStorage = {
      getItem: () => 'not-a-number',
    }
    expect(shouldProbe(Date.now(), invalidValueStorage)).toBe(true)
  })

  it('markProbed 在 setItem 抛错时静默不抛', () => {
    const faultyStorage = {
      setItem: () => {
        throw new Error('QuotaExceeded')
      },
    }
    expect(() => markProbed(Date.now(), faultyStorage)).not.toThrow()
  })
})
