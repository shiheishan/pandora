/**
 * [INPUT]: 依赖 vitest，依赖 ./model 的第 ④ 步纯函数，依赖 ./api 的第 ④ 步 schema
 * [OUTPUT]: 用户组 / 批量运营 / 设备策略 / 流量重置的纯逻辑与 schema 单元测试
 * [POS]: admin/screens/users 第 ④ 步的测试，与 model.test.ts（列表与抽屉）并列：用户组删除拦截（R104 节点池名单优先）、「可用节点池」与「被引用」、设备策略请求体与 R103 识别窗口、批量筛选表单到 BulkFilter 与导出 query、批量生成的前端校验（与 adminops.GenerateUsers 同规则）、接近上限与在线格、重置日志的操作人文案、重置原因、手动重置挑哪条订阅、按邮箱精确匹配；schema 守住 omitempty 键、封闭枚举与「口令只回一次」的形状
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { describe, expect, it } from 'vitest'
import { bulkPreviewSchema, devicesSchema, generatedSchema, resetDoneSchema, resetLogsSchema, resetStatsSchema, userGroupsSchema, type OnlineDevice } from './api'
import {
  bulkFilter,
  devicePolicyBody,
  exclusivePoolsLabel,
  exactEmail,
  exportQuery,
  generatedRows,
  generateProblems,
  groupBlocker,
  groupRefs,
  nearLimit,
  noteProblem,
  pips,
  resetActor,
  resettableSub,
  type GenerateForm,
} from './model'

describe('用户组', () => {
  const g = { users: 0, plans: 0, prices: 0, coupons: 0, exclusive_pools: [] as Array<{ id: string; name: string }> }
  it('按后端 409 的先后顺序说明为什么不能删（R104 节点池名单排最前）', () => {
    expect(groupBlocker(g)).toBeNull()
    expect(groupBlocker({ ...g, users: 3, exclusive_pools: [{ id: 'p', name: '专属线路 A' }] })).toBe('节点池「专属线路 A」限定了这个组，先在节点池里把它移出名单')
    expect(groupBlocker({ ...g, users: 3, plans: 1 })).toBe('组内还有 3 位用户，先把他们移出去')
    expect(groupBlocker({ ...g, plans: 2, coupons: 1 })).toBe('还有 2 个套餐按这个组控制可见性，先解除')
    expect(groupBlocker({ ...g, prices: 1 })).toContain('专属价格')
    expect(groupBlocker({ ...g, coupons: 4 })).toContain('4 张优惠券')
  })

  it('R104「可用节点池」：列名单里的池，空是破折号', () => {
    expect(exclusivePoolsLabel(g)).toBe('—')
    expect(exclusivePoolsLabel({ exclusive_pools: [{ id: 'a', name: '专线 A' }, { id: 'b', name: '灰度' }] })).toBe('专线 A、灰度')
    expect(userGroupsSchema.parse({ groups: [{ id: 'g', code: 'vip', name: 'VIP', description: '', users: 0, plans: 0, prices: 0, coupons: 0, exclusive_pools: null }] }).groups[0]!.exclusive_pools).toEqual([])
  })

  it('「被引用」只列不为 0 的，都为 0 时是破折号', () => {
    expect(groupRefs(g)).toBe('—')
    expect(groupRefs({ plans: 2, prices: 0, coupons: 1 })).toBe('套餐 2 · 优惠券 1')
  })
})

describe('设备策略（R103）', () => {
  it('strict 才带宽容值；识别窗口改了才带（省略 = 不改）', () => {
    expect(devicePolicyBody({ mode: 'loose', grace: '1', window: 5 }, 5)).toEqual({ mode: 'loose' })
    expect(devicePolicyBody({ mode: 'strict', grace: '2', window: 30 }, 5)).toEqual({ mode: 'strict', grace: 2, window_minutes: 30 })
    expect(devicePolicyBody({ mode: 'strict', grace: '9', window: 5 }, 5)).toBeNull()
    expect(devicePolicyBody({ mode: 'strict', grace: '', window: 5 }, 5)).toBeNull()
  })
})

describe('批量筛选', () => {
  const empty = { plan: '', status: '', expiry: '', group: '' } as const
  it('空条件不带任何键；已过期走 sub_state，停用 / 封禁各是单值 status', () => {
    expect(bulkFilter(empty)).toEqual({})
    expect(bulkFilter({ ...empty, status: 'expired' })).toEqual({ sub_state: 'expired' })
    expect(bulkFilter({ ...empty, status: 'banned' })).toEqual({ status: 'banned' })
    expect(bulkFilter({ plan: 'p1', status: 'active', expiry: '7', group: 'g1' })).toEqual({ plan_id: 'p1', status: 'active', expires_within_days: 7, group_id: 'g1' })
  })

  it('导出用同名 query 参数，数字转字符串', () => {
    expect(exportQuery({ status: 'active', expires_within_days: 30 })).toEqual({ status: 'active', expires_within_days: '30' })
  })
})

describe('批量生成', () => {
  const ok: GenerateForm = { count: '20', prefix: 'Dealer-01', domain: ' Example.com ', group: '', reason: '线下渠道预制账号' }
  it('合法输入没有问题（前缀与域名先转小写再校验，与后端一致）', () => {
    expect(generateProblems(ok)).toEqual({})
  })

  it('数量 1–500、前缀字符集与长度、域名、原因 5–500 字', () => {
    expect(generateProblems({ ...ok, count: '0' }).count).toBeDefined()
    expect(generateProblems({ ...ok, count: '501' }).count).toBeDefined()
    expect(generateProblems({ ...ok, count: '2.5' }).count).toBeDefined()
    expect(generateProblems({ ...ok, prefix: '' }).prefix).toBeDefined()
    expect(generateProblems({ ...ok, prefix: 'a_b' }).prefix).toBeDefined()
    expect(generateProblems({ ...ok, prefix: 'a'.repeat(21) }).prefix).toBeDefined()
    expect(generateProblems({ ...ok, domain: 'localhost' }).domain).toBeDefined()
    expect(generateProblems({ ...ok, domain: 'a.b' }).domain).toBeDefined()
    expect(generateProblems({ ...ok, reason: '太短' }).reason).toBeDefined()
    expect(generateProblems({ ...ok, reason: '原'.repeat(501) }).reason).toBeDefined()
  })

  it('本地 CSV 第一行是表头', () => {
    expect(generatedRows([{ email: 'a@x.io', password: 'p' }])).toEqual([
      ['邮箱', '初始密码'],
      ['a@x.io', 'p'],
    ])
  })
})

describe('设备策略', () => {
  const d = (online: number, limit: number): OnlineDevice => ({ subscription_id: `${online}-${limit}`, email: 'a@b.c', plan: '标准版', limit, online, nodes: 1, overridden: false, exceeded: false, last_seen_at: null })
  it('有上限且在线已到上限才算「接近或超出」；不限（0）不算', () => {
    expect(nearLimit([d(3, 3), d(2, 3), d(9, 0), d(5, 3)]).map((x) => x.subscription_id)).toEqual(['3-3', '5-3'])
  })

  it('在线格：额度内实心、超出危险色、空位灰，最多 12 格', () => {
    expect(pips(2, 3)).toEqual(['on', 'on', 'off'])
    expect(pips(4, 3)).toEqual(['on', 'on', 'on', 'over'])
    expect(pips(30, 20)).toHaveLength(12)
  })
})

describe('流量重置', () => {
  it('操作人：有邮箱用邮箱并追加说明；没有时按原因写成系统', () => {
    expect(resetActor({ reason: 'manual', actor_email: 'ops@x.io', note: '工单 #1' })).toBe('ops@x.io · 工单 #1')
    expect(resetActor({ reason: 'manual', actor_email: 'ops@x.io' })).toBe('ops@x.io')
    expect(resetActor({ reason: 'renewal' })).toBe('系统 · 续费')
    expect(resetActor({ reason: 'cycle_roll' })).toBe('系统 · 周期滚动')
    expect(resetActor({ reason: 'plan_change' })).toBe('系统 · 变更套餐')
  })

  it('重置原因 5–500 字，按字符计', () => {
    expect(noteProblem('四个字啊')).not.toBeNull()
    expect(noteProblem('  补偿断线时长  ')).toBeNull()
    expect(noteProblem('字'.repeat(501))).not.toBeNull()
  })

  it('只挑 status=active 里到期最晚的一条；试用、宽限都不算', () => {
    const s = (status: 'active' | 'trialing' | 'grace', end: string | null) => ({ status, current_period_end: end, id: `${status}-${end}` })
    expect(resettableSub([s('trialing', '2026-12-01'), s('grace', '2026-12-02')])).toBeUndefined()
    expect(resettableSub([s('active', '2026-10-01'), s('active', '2026-11-01'), s('trialing', '2027-01-01')])?.id).toBe('active-2026-11-01')
  })

  it('按邮箱找人只认完全相等（不分大小写），模糊命中不算', () => {
    const rows = [{ email: 'k.liu@gmail.com' }, { email: 'k.liu@gmail.com.cn' }]
    expect(exactEmail(rows, ' K.Liu@Gmail.com ')).toBe(rows[0])
    expect(exactEmail(rows, 'k.liu@gmail')).toBeUndefined()
    expect(exactEmail(rows, '')).toBeUndefined()
  })
})

describe('schema', () => {
  it('重置日志：plan_name / actor_email / note 可整键省略，reason 是封闭枚举（含 R38 plan_change）', () => {
    const log = { id: 'l', user_email: 'a@b.c', metric: 'traffic.bytes', reason: 'plan_change', consumed_before: 1, created_at: '2026-09-24T00:00:00Z' }
    expect(resetLogsSchema.safeParse({ logs: [log], total: 1 }).success).toBe(true)
    expect(resetLogsSchema.safeParse({ logs: [{ ...log, reason: 'admin' }], total: 1 }).success).toBe(false)
    expect(resetStatsSchema.safeParse({ last_30_days: 3, by_reason: { manual: 3 }, freed_bytes: 10, manual_count: 3 }).success).toBe(true)
    expect(resetDoneSchema.safeParse({ reset: true, freed_bytes: 0 }).success).toBe(true)
  })

  it('设备：模式只有 loose / strict，last_seen_at 可为 null', () => {
    const row = { subscription_id: 's', email: 'a@b.c', plan: '标准版', limit: 3, online: 3, nodes: 1, overridden: false, exceeded: false, last_seen_at: null }
    expect(devicesSchema.safeParse({ devices: [row], mode: 'strict', grace: 1, window_minutes: 30 }).success).toBe(true)
    expect(devicesSchema.safeParse({ devices: [row], mode: 'kick', grace: 1, window_minutes: 5 }).success).toBe(false)
    // R103：窗口只有 5 / 10 / 30 / 60 四档
    expect(devicesSchema.safeParse({ devices: [row], mode: 'strict', grace: 1, window_minutes: 15 }).success).toBe(false)
    expect(devicesSchema.safeParse({ devices: [row], mode: 'strict', grace: 1 }).success).toBe(false)
  })

  it('批量预览要 sample_rows；生成结果是邮箱 + 口令数组', () => {
    expect(bulkPreviewSchema.safeParse({ total: 1, samples: ['a@b.c'], sample_rows: [{ email: 'a@b.c', plan_name: null, current_period_end: null }] }).success).toBe(true)
    expect(bulkPreviewSchema.safeParse({ total: 1, samples: ['a@b.c'] }).success).toBe(false)
    expect(generatedSchema.safeParse({ count: 1, users: [{ email: 'a@b.c', password: 'x' }], warning: '只显示一次' }).success).toBe(true)
  })
})
