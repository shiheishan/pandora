import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../core/api'
import { renameError, rotateWaitText } from './model'

describe('我的套餐 · 弹层', () => {
  it('换新链接超限：按份限频的剩余冷却写成「刚换过，N 分钟后才能再换」', () => {
    expect(rotateWaitText(new ApiError({ status: 429, code: 'rate_limited', message: '操作太频繁，请 9 分钟后再试' }))).toBe('刚换过，9 分钟后才能再换')
    expect(rotateWaitText(new ApiError({ status: 429, code: 'rate_limited', message: 'slow down' }))).toBe('刚换过，稍后才能再换')
    expect(rotateWaitText(new ApiError({ status: 409, code: 'conflict', message: 'x' }))).toBeNull()
  })

  it('改名失败：校验错误落到输入框（fields.label），撞名用服务端那句', () => {
    expect(renameError(new ApiError({ status: 422, code: 'validation_failed', message: '参数不合法', fields: { label: '名字最多 16 个字' } }))).toBe('名字最多 16 个字')
    expect(renameError(new ApiError({ status: 409, code: 'conflict', message: '这个名字已经用在「我的 · 标准版」上了', fields: { label: '换一个名字' } }))).toBe('这个名字已经用在「我的 · 标准版」上了')
  })
})
