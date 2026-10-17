import { describe, expect, it } from 'vitest'
import { banSkippedText, hasAnyFilter } from './BulkBanForm'
import { jobFilename, jobFinished, jobPercent, jobResultView } from './jobs'
import { bulkPreviewSchema, devicesSchema, generationJobSchema, resetDoneSchema, trafficGrantedSchema, userGroupsSchema, type OnlineDevice } from './api'
import {
  assignableGroups,
  bulkFilter,
  devicePolicyBody,
  exclusivePoolsLabel,
  exactEmail,
  exportQuery,
  generateProblems,
  grantReasonProblem,
  GROUP_DISABLE_NOTICE,
  groupBlocker,
  GROUP_DELETE_NOTICE,
  groupOptionLabel,
  groupRefs,
  isOrderTarget,
  nearLimit,
  noteProblem,
  parseGrantGB,
  pips,
  subName,
  subLabel,
  type GenerateForm,
} from './model'

describe('用户组', () => {
  const g = { status: 'active' as const, users: 0, plans: 0, coupons: 0, exclusive_pools: [] as Array<{ id: string; name: string }> }
  it('按后端 409 的先后顺序说明为什么不能删（R104 节点池名单排最前）', () => {
    expect(groupBlocker(g)).toBeNull()
    expect(groupBlocker({ ...g, users: 3, exclusive_pools: [{ id: 'p', name: '专属线路 A' }] })).toBe('节点池「专属线路 A」限定了这个组，先在节点池里把它移出名单')
    expect(groupBlocker({ ...g, users: 3, plans: 1 })).toBe('组内还有 3 位用户，先把他们移出去')
    expect(groupBlocker({ ...g, plans: 2, coupons: 1 })).toBe('还有 2 个套餐按这个组控制可见性，先解除')
    expect(groupBlocker({ ...g, coupons: 4 })).toContain('4 张优惠券')
  })

  it('R104「可用节点池」：列名单里的池，空是破折号', () => {
    expect(exclusivePoolsLabel(g)).toBe('—')
    expect(exclusivePoolsLabel({ exclusive_pools: [{ id: 'a', name: '专线 A' }, { id: 'b', name: '灰度' }] })).toBe('专线 A、灰度')
    expect(userGroupsSchema.parse({ groups: [{ id: 'g', code: 'vip', name: 'VIP', description: '', status: 'active', users: 0, plans: 0, coupons: 0, exclusive_pools: null }] }).groups[0]!.exclusive_pools).toEqual([])
    expect(userGroupsSchema.safeParse({ groups: [{ id: 'g', code: 'vip', name: 'VIP', description: '', users: 0, plans: 0, coupons: 0, exclusive_pools: null }] }).success).toBe(false)
  })

  it('「被引用」只列不为 0 的，都为 0 时是破折号', () => {
    expect(groupRefs(g)).toBe('—')
    expect(groupRefs({ plans: 2, coupons: 1 })).toBe('套餐 2 · 优惠券 1')
  })

  it('停用组：分配下拉隐藏，但保留当前值并标「（已停用）」', () => {
    const list = [{ id: 'a', name: 'VIP', status: 'active' }, { id: 'b', name: '旧渠道', status: 'disabled' }]
    expect(assignableGroups(list).map((x) => x.id)).toEqual(['a'])
    expect(assignableGroups(list, ['b']).map((x) => x.id)).toEqual(['a', 'b'])
    expect(groupOptionLabel(list[1]!)).toBe('旧渠道（已停用）')
    expect(groupOptionLabel(list[0]!)).toBe('VIP')
  })

  it('删组、停用确认的正文', () => {
    expect(GROUP_DELETE_NOTICE).not.toContain('专属价格')
    expect(GROUP_DISABLE_NOTICE).toContain('已有成员不受影响')
    expect(GROUP_DISABLE_NOTICE).not.toContain('专属价格')
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

  it('hasAnyFilter 检查筛选条件是否为空', () => {
    expect(hasAnyFilter({})).toBe(false)
    expect(hasAnyFilter({ status: 'active' })).toBe(true)
    expect(hasAnyFilter({ group_id: 'g1' })).toBe(true)
    expect(hasAnyFilter({ plan_id: 'p1' })).toBe(true)
    expect(hasAnyFilter({ expires_within_days: 7 })).toBe(true)
    expect(hasAnyFilter({ sub_state: 'expired' })).toBe(true)
  })

  it('banSkippedText 格式化跳过文案', () => {
    expect(banSkippedText({})).toBe('')
    expect(banSkippedText({ self: 0, admin: 0 })).toBe('')
    expect(banSkippedText({ admin: 2 })).toBe('跳过：管理员 2')
    // 顺序：self, admin, already_banned, deleted
    expect(banSkippedText({ already_banned: 5, admin: 2, self: 1, deleted: 3 })).toBe('跳过：自己 1、管理员 2、已封禁 5、已注销 3')
    // 未知键原样显示键名，排在已知键后面或按字符顺序
    expect(banSkippedText({ custom_reason: 4, admin: 1 })).toBe('跳过：管理员 1、custom_reason 4')
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

  it('任务进度：百分比取整、结束即停轮询、文件名带前缀与日期', () => {
    expect(jobPercent({ total: 3, completed: 1 })).toBe(33)
    expect(jobPercent({ total: 3, completed: 3 })).toBe(100)
    expect(jobPercent({ total: 0, completed: 0 })).toBe(0)
    expect(jobFinished({ status: 'running' })).toBe(false)
    expect(jobFinished({ status: 'failed' })).toBe(true)
    expect(jobFilename({ email_prefix: 'dealer', created_at: '2026-10-07T08:00:00Z' })).toBe('users-dealer-2026-10-07.csv')
  })

  it('生成记录的结果列：成功但有没生成的标警示并写个数', () => {
    expect(jobResultView({ status: 'succeeded', failed: 3 })).toEqual({ label: '完成 · 3 个未生成', tone: 'warn' })
    expect(jobResultView({ status: 'succeeded', failed: 0 })).toEqual({ label: '已完成', tone: 'ok' })
    expect(jobResultView({ status: 'running', failed: 0 })).toEqual({ label: '生成中', tone: 'info' })
    expect(jobResultView({ status: 'failed', failed: 2 })).toEqual({ label: '已停止', tone: 'danger' })
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
  it('重置原因 5–500 字，按字符计', () => {
    expect(noteProblem('四个字啊')).not.toBeNull()
    expect(noteProblem('  补偿断线时长  ')).toBeNull()
    expect(noteProblem('字'.repeat(501))).not.toBeNull()
  })

  it('订阅的称呼与下拉标签带备注名', () => {
    expect(subName({ label: null, plan_name: '标准版' })).toBe('标准版')
    expect(subName({ label: '妈妈的 iPad', plan_name: '标准版' })).toBe('「妈妈的 iPad」标准版')
    expect(subLabel({ label: '妈妈的 iPad', plan_name: '标准版', plan_version: 2, status: 'active' })).toBe('「妈妈的 iPad」标准版 · v2 · 生效中')
  })

  it('「给这份开单」只对还在用或还能续的订阅有意义', () => {
    const now = new Date('2026-10-07T00:00:00Z')
    const s = (status: 'active' | 'expired', end: string | null) => ({ status, current_period_end: end })
    expect(isOrderTarget(s('active', '2026-11-01T00:00:00Z'), now)).toBe(true)
    expect(isOrderTarget(s('expired', '2026-09-20T00:00:00Z'), now)).toBe(true)
    expect(isOrderTarget(s('expired', '2026-08-01T00:00:00Z'), now)).toBe(false)
    expect(isOrderTarget(s('expired', '2026-08-15T00:00:00Z'), now)).toBe(false)
  })

  it('按邮箱找人只认完全相等（不分大小写），模糊命中不算', () => {
    const rows = [{ email: 'k.liu@gmail.com' }, { email: 'k.liu@gmail.com.cn' }]
    expect(exactEmail(rows, ' K.Liu@Gmail.com ')).toBe(rows[0])
    expect(exactEmail(rows, 'k.liu@gmail')).toBeUndefined()
    expect(exactEmail(rows, '')).toBeUndefined()
  })
})

describe('schema', () => {
  it('重置流量完成：清掉的字节数', () => {
    expect(resetDoneSchema.safeParse({ reset: true, freed_bytes: 0 }).success).toBe(true)
    expect(resetDoneSchema.safeParse({ reset: true, freed_bytes: 1024 }).success).toBe(true)
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
    const job = {
      id: 'j1', actor_id: 'a1', status: 'running', total: 3, completed: 1, failed: 0, email_prefix: 'dealer', email_domain: 'example.com',
      group_id: null, reason: '线下渠道预制', error: null, result_available: true, result_expires_at: null,
      created_at: '2026-10-07T08:00:00Z', started_at: '2026-10-07T08:00:01Z', finished_at: null,
    }
    expect(generationJobSchema.safeParse(job).success).toBe(true)
    // 后端没有 omitempty：缺字段即判不符
    expect(generationJobSchema.safeParse({ ...job, finished_at: undefined }).success).toBe(false)
    expect(generationJobSchema.safeParse({ ...job, status: 'done' }).success).toBe(false)
  })

  it('加流量：GB 换字节、上限 10240 GB、原因 5 到 500 字，响应形状符合 trafficGrantedSchema', () => {
    expect(parseGrantGB('10')).toBe(10 * 1024 ** 3)
    expect(parseGrantGB('10240')).toBe(10240 * 1024 ** 3)
    for (const bad of ['0', '10241', '1.5', '-1', '', 'abc']) expect(parseGrantGB(bad)).toBeNull()
    expect(grantReasonProblem('短')).not.toBeNull()
    expect(grantReasonProblem('补偿线路故障')).toBeNull()
    expect(grantReasonProblem('原'.repeat(501))).not.toBeNull()

    const ok = { subscription_id: 's', user_id: 'u', user_email: 'a@b.c', granted_bytes: 10 * 1024 ** 3, bonus_bytes: 10 * 1024 ** 3 }
    expect(trafficGrantedSchema.safeParse(ok).success).toBe(true)
    // 带旧字段 remaining_bytes_total 而缺 bonus_bytes 的判为不符
    expect(
      trafficGrantedSchema.safeParse({ subscription_id: 's', user_id: 'u', user_email: 'a@b.c', grant_id: 'g', granted_bytes: 1, remaining_bytes_total: 1 }).success,
    ).toBe(false)
  })
})
