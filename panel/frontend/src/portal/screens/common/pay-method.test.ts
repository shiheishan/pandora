import { describe, expect, it } from 'vitest'
import { belowMethodMinimum, usableMethods, type PaymentMethod } from './catalog'
import { pickMethod } from './PayFlow'

// 付不了这笔钱的方式不能被选成缺省（align-pay 审查必修）
function method(over: Partial<PaymentMethod> = {}): PaymentMethod {
  return { provider: 'epay', method: 'alipay', label: '支付宝', fee_fixed: 0, fee_percent: '0', min_amount: 100, ...over }
}

describe('belowMethodMinimum / usableMethods', () => {
  const m = method({ min_amount: 100 })

  it('应付等于最低额可用，低 1 分不可用', () => {
    expect(belowMethodMinimum(m, 100)).toBe(false)
    expect(belowMethodMinimum(m, 99)).toBe(true)
  })

  it('应付为 0 或未知时不算付不了', () => {
    expect(belowMethodMinimum(m, 0)).toBe(false)
    expect(belowMethodMinimum(m, undefined)).toBe(false)
  })

  it('筛掉付不了的，顺序不变', () => {
    const a = method({ method: 'a', min_amount: 500 })
    const b = method({ method: 'b', min_amount: 100 })
    const c = method({ method: 'c', min_amount: 0 })
    expect(usableMethods([a, b, c], 200).map((x) => x.method)).toEqual(['b', 'c'])
  })
})

describe('pickMethod 带应付', () => {
  const high = method({ method: 'wxpay', label: '微信', min_amount: 1000 })
  const low = method({ method: 'alipay', min_amount: 100 })

  it('key 指向付不了的方式时回第一个付得了的', () => {
    expect(pickMethod([high, low], 'epay:wxpay', 500)).toBe(low)
  })

  it('key 指向付得了的方式时照选', () => {
    expect(pickMethod([high, low], 'epay:wxpay', 1000)).toBe(high)
  })

  it('全部付不了时为 null', () => {
    expect(pickMethod([high, low], '', 50)).toBeNull()
  })

  it('应付未知时不筛', () => {
    expect(pickMethod([high, low], '')).toBe(high)
  })
})
