import { describe, expect, it } from 'vitest'
import type { MockContext } from '../dev/mock/types'
import { opsRoutes, type UsersStore } from '../dev/mock/admin/users-ops'
import { userStore, type Group } from '../dev/mock/admin/users'

// 删组的两条专属价分支（与 Go 同口径）：没卖过的随组删除，卖过的回 409 提示改用停用。
// 种子组都带套餐或优惠券引用，走不到这两条分支，所以直接拿路由表配一份最小的假数据。
describe('mock api · 删组与专属价', () => {
  const group = (id: string, prices: number): Group => ({ id, code: `g-${id.slice(-1)}`, name: id, description: '', status: 'active', plans: 0, prices, coupons: 0 })
  const UNSOLD = '9c0e1a2b-4444-4b00-8000-000000000001'
  const SOLD = '9c0e1a2b-4444-4b00-8000-000000000002'

  const setup = () => {
    const groups = [group(UNSOLD, 2), group(SOLD, 1)]
    const store: UsersStore = {
      // 种子用户（批量运营的日志要用）；都不在这两个组里
      users: userStore,
      groups,
      soldPriceGroups: new Set([SOLD]),
      current: () => undefined,
      hasSubState: () => false,
      effectiveLimit: () => 0,
      exclusivePools: () => [],
    }
    const del = opsRoutes(store)['DELETE /v1/user-groups/:id']!
    const call = async (id: string) => {
      let out: { status: number; message?: string } = { status: 0 }
      const ctx = {
        params: { id },
        fail: (status: number, _code: string, message: string) => {
          out = { status, message }
        },
        send: (status: number) => {
          out = { status }
        },
      } as unknown as MockContext
      await del(ctx)
      return out
    }
    return { groups, call }
  }

  it('没卖过的专属价随组删除', async () => {
    const { groups, call } = setup()
    expect((await call(UNSOLD)).status).toBe(200)
    expect(groups.map((g) => g.id)).toEqual([SOLD])
  })

  it('卖过的专属价挡住删组，提示改用停用，组还在', async () => {
    const { groups, call } = setup()
    const res = await call(SOLD)
    expect(res.status).toBe(409)
    expect(res.message).toContain('已经有人买过')
    expect(res.message).toContain('停用')
    expect(groups.some((g) => g.id === SOLD)).toBe(true)
  })
})
