import { describe, expect, it } from 'vitest'
import type { HomeSettings } from './schemas'
import {
  homeChanges,
  moveFaq,
  normalizeFaq,
  normalizeMultiline,
  validateHome,
  type FaqDraft,
} from './homeForm'

const base: HomeSettings = {
  enabled: true,
  intro: '虚构机场简介',
  faq: [{ q: '虚构问题一', a: '虚构回答一' }],
}

describe('normalizeMultiline', () => {
  it('规整换行与首尾空白', () => {
    expect(normalizeMultiline(' a\r\nb\rc ')).toBe('a\nb\nc')
  })
})

describe('homeChanges', () => {
  it('草稿全 null 时无改动', () => {
    expect(homeChanges(base, { enabled: null, intro: null, faq: null })).toEqual({})
  })

  it('只改 enabled', () => {
    expect(homeChanges(base, { enabled: false, intro: null, faq: null })).toEqual({ enabled: false })
  })

  it('intro 仅首尾空格不算改', () => {
    expect(homeChanges(base, { enabled: null, intro: '  虚构机场简介  ', faq: null })).toEqual({})
  })

  it('faq 换顺序要带整组', () => {
    const current: HomeSettings = {
      ...base,
      faq: [
        { q: '甲', a: '答甲' },
        { q: '乙', a: '答乙' },
      ],
    }
    const draft: FaqDraft[] = [
      { key: '1', q: '乙', a: '答乙' },
      { key: '2', q: '甲', a: '答甲' },
    ]
    expect(homeChanges(current, { enabled: null, intro: null, faq: draft })).toEqual({
      faq: [{ q: '乙', a: '答乙' }, { q: '甲', a: '答甲' }],
    })
  })
})

describe('validateHome', () => {
  it('intro 超 500 字', () => {
    expect(validateHome({ intro: '虚'.repeat(501) })).toEqual({ intro: '介绍最多 500 个字' })
  })

  it('intro 含控制字符', () => {
    expect(validateHome({ intro: '虚\u0007构' })).toEqual({ intro: '介绍不能包含控制字符' })
  })

  it('faq 超 20 条', () => {
    const faq = Array.from({ length: 21 }, (_, i) => ({ q: `问${i}`, a: `答${i}` }))
    expect(validateHome({ faq })).toEqual({ faq: '常见问题最多 20 条' })
  })

  it('faq.0.q 空', () => {
    expect(validateHome({ faq: [{ q: '', a: '虚构回答' }] })).toEqual({ 'faq.0.q': '问题必填' })
  })

  it('faq.0.q 超 100 字', () => {
    expect(validateHome({ faq: [{ q: '问'.repeat(101), a: '虚构回答' }] })).toEqual({ 'faq.0.q': '问题最多 100 个字' })
  })

  it('faq.0.q 含换行', () => {
    expect(validateHome({ faq: [{ q: '问\n题', a: '虚构回答' }] })).toEqual({
      'faq.0.q': '问题不能包含换行或控制字符',
    })
  })

  it('faq.0.a 空', () => {
    expect(validateHome({ faq: [{ q: '虚构问题', a: '' }] })).toEqual({ 'faq.0.a': '回答必填' })
  })

  it('faq.0.a 超 500 字', () => {
    expect(validateHome({ faq: [{ q: '虚构问题', a: '答'.repeat(501) }] })).toEqual({ 'faq.0.a': '回答最多 500 个字' })
  })

  it('faq.0.a 含控制字符', () => {
    expect(validateHome({ faq: [{ q: '虚构问题', a: '答\u0001' }] })).toEqual({ 'faq.0.a': '回答不能包含控制字符' })
  })
})

describe('moveFaq', () => {
  const list = ['a', 'b', 'c']

  it('上移', () => {
    expect(moveFaq(list, 1, -1)).toEqual(['b', 'a', 'c'])
  })

  it('下移', () => {
    expect(moveFaq(list, 1, 1)).toEqual(['a', 'c', 'b'])
  })

  it('越界不变', () => {
    expect(moveFaq(list, 0, -1)).toEqual(['a', 'b', 'c'])
    expect(moveFaq(list, 2, 1)).toEqual(['a', 'b', 'c'])
  })
})

describe('normalizeFaq', () => {
  it('规整 q 与 a', () => {
    expect(normalizeFaq([{ key: 'k', q: ' 问 ', a: ' 答\r\n行 ' }])).toEqual([{ q: '问', a: '答\n行' }])
  })
})
