import { describe, expect, it } from 'vitest'
import { feeLine, feeRule, hasFee, surchargeNote, trimPercent } from './fee'

describe('trimPercent', () => {
  it('正确裁剪百分比字符串末尾的 0', () => {
    expect(trimPercent('2.50')).toBe('2.5')
    expect(trimPercent('0.00')).toBe('0')
    expect(trimPercent('10.00')).toBe('10')
    expect(trimPercent('0.5')).toBe('0.5')
    expect(trimPercent('12')).toBe('12')
    expect(trimPercent('invalid')).toBe('0')
  })
})

describe('hasFee', () => {
  it('判断是否有手续费', () => {
    expect(hasFee({ fee_fixed: 0, fee_percent: '0.00' })).toBe(false)
    expect(hasFee({ fee_fixed: 30, fee_percent: '0.00' })).toBe(true)
    expect(hasFee({ fee_fixed: 0, fee_percent: '2.50' })).toBe(true)
    expect(hasFee({ fee_fixed: 30, fee_percent: '2.50' })).toBe(true)
  })
})

describe('feeRule', () => {
  it('无手续费返回空字符串', () => {
    expect(feeRule({ fee_fixed: 0, fee_percent: '0.00' })).toBe('')
  })

  it('只有固定手续费', () => {
    expect(feeRule({ fee_fixed: 30, fee_percent: '0.00' })).toBe('手续费 ¥0.30')
  })

  it('只有比例手续费', () => {
    expect(feeRule({ fee_fixed: 0, fee_percent: '2.50' })).toBe('手续费 2.5%')
  })

  it('固定与比例手续费都有', () => {
    expect(feeRule({ fee_fixed: 30, fee_percent: '2.50' })).toBe('手续费 2.5% + ¥0.30')
  })
})

describe('feeLine', () => {
  it('带 fee 与 total 且 fee > 0 时返回具体计算的手续费与合计', () => {
    expect(
      feeLine({
        fee_fixed: 30,
        fee_percent: '2.50',
        fee: 55,
        total: 1055,
      }),
    ).toBe('手续费 ¥0.55，合计 ¥10.55')
  })

  it('带 fee 与 total 且 fee === 0 时返回空字符串', () => {
    expect(
      feeLine({
        fee_fixed: 0,
        fee_percent: '0.00',
        fee: 0,
        total: 1000,
      }),
    ).toBe('')
  })

  it('没带 fee 时退回 feeRule(m)', () => {
    expect(feeLine({ fee_fixed: 30, fee_percent: '2.50' })).toBe('手续费 2.5% + ¥0.30')
    expect(feeLine({ fee_fixed: 0, fee_percent: '0.00' })).toBe('')
  })
})

describe('surchargeNote', () => {
  it('surcharge > 0 时返回含手续费与订单金额提示', () => {
    expect(surchargeNote(55, 1055)).toBe('含手续费 ¥0.55，订单 ¥10.00')
  })

  it('surcharge <= 0 时返回空字符串', () => {
    expect(surchargeNote(0, 1000)).toBe('')
    expect(surchargeNote(-10, 1000)).toBe('')
  })
})
