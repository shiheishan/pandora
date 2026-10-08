import { PROTO_PLANS, type CatalogPlan } from './catalog.ts'
import { makeSub, type PortalState, type SubFixture } from './fixtures.ts'
import type { ProtoScenario } from './scenario.ts'

// ---------------------------------------------------------------------------
// 购买流程原型（.claude/purchase-proto/index.html）的 11 个场景，数据照原型 SCENARIOS：
// 原型的「今天」固定 10 月 7 日，这里换成相对今天的天数（10/19 = 12 天后，10/4 = 3 天前）。
// 每份一期 30 天、按月价付费；已用流量按 GB 给。给新手做首次点击测试，任务见原型各场景的 hint。
//   proto-s1   一份标准版，流量只剩 8G
//   proto-s2   小王两份：「我的」标准版流量快用完，「妈妈的 iPad」基础版 5 天后到期，余额 ¥8.50
//   proto-s3   已有标准版，从选购页开始
//   proto-s4   标准版还剩 25 天、用了 20G：换大 / 换便宜
//   proto-s5a  兑换卡：只有一份（卡号 A7K2-STD1-M9QX / B3P8-PRO1-W4RT / C5D3-D030-H8NE / D9R1-RSET-J2LU）
//   proto-s5b  兑换卡：小王两份
//   proto-s6   妈妈那份的链接近 24 小时有 7 个地方在用（超过 2 台上限）
//   proto-s7   余额 ¥12，续费 ¥30
//   proto-s7b  余额 ¥50，够付
//   proto-s7c  余额 ¥29.50，剩 5 毛低于支付最低额
//   proto-s8   标准版 3 天前已过期
//   proto-legacy  小王两份，升级前买的 80G 流量包挂在「我的」上，能挪一次
// ---------------------------------------------------------------------------
const [BASIC, STD] = PROTO_PLANS as [CatalogPlan, CatalogPlan, CatalogPlan]
const DAY_MS = 86_400_000

interface ProtoSub {
  plan?: CatalogPlan
  /** 距到期的天数；≤ 0 为已过期 */
  days?: number
  used?: number
  tail?: string
  label?: string
  online?: number
  sources?: number
}

function sub(o: ProtoSub): SubFixture {
  const plan = o.plan ?? STD
  const days = o.days ?? 12
  return makeSub({
    planId: plan.id,
    priceId: plan.prices[0]!.id,
    status: days <= 0 ? 'expired' : 'active',
    usedGiB: o.used ?? 40,
    elapsedDays: 30 - days,
    resetInDays: Math.max(1, days),
    expiresInDays: days,
    online: o.online ?? 1,
    sources: o.sources ?? 1,
    label: o.label ?? null,
    tail: o.tail ?? 'a3f9',
  })
}

const xiaowang = (leak: boolean) => ({
  balance: 850,
  subs: [
    sub({ label: '我的', days: 23, used: 93, tail: 'a3f9', online: 2, sources: 2 }),
    sub({ label: '妈妈的 iPad', plan: BASIC, days: 5, used: 12, tail: '7c21', online: 1, sources: leak ? 7 : 1 }),
  ],
})
const single = (o: ProtoSub, balance = 0) => ({ balance, subs: [sub(o)] })

const DATA: Readonly<Record<ProtoScenario, () => { balance: number; subs: SubFixture[] }>> = {
  'proto-s1': () => single({ used: 92 }),
  'proto-s2': () => xiaowang(false),
  'proto-s3': () => single({}),
  'proto-s4': () => single({ days: 25, used: 20 }),
  'proto-s5a': () => single({ used: 60 }),
  'proto-s5b': () => xiaowang(false),
  'proto-s6': () => xiaowang(true),
  'proto-s7': () => single({}, 1200),
  'proto-s7b': () => single({}, 5000),
  'proto-s7c': () => single({}, 2950),
  'proto-s8': () => single({ days: -3, used: 47 }),
  // 用户 10-07：升级前买的 80G 流量包迁移时挂到了到期最晚的「我的」，可以自己挪一次到「妈妈的 iPad」
  'proto-legacy': () => {
    const d = xiaowang(false)
    d.subs[0]!.packBytes = 80 * 1024 ** 3
    d.subs[0]!.legacyPackBytes = 80 * 1024 ** 3
    return d
  },
}

/** 每个场景开始时进入的页面（原型 start；兑换场景从钱包的兑换页开始） */
export const PROTO_START: Readonly<Record<ProtoScenario, string>> = {
  'proto-s1': '/subs',
  'proto-s2': '/subs',
  'proto-s3': '/plans',
  'proto-s4': '/subs',
  'proto-s5a': '/wallet/redeem',
  'proto-s5b': '/wallet/redeem',
  'proto-s6': '/subs',
  'proto-s7': '/subs',
  'proto-s7b': '/subs',
  'proto-s7c': '/subs',
  'proto-s8': '/subs',
  'proto-legacy': '/subs',
}

export function buildProto(s: ProtoScenario): PortalState {
  const { balance, subs } = DATA[s]()
  return {
    subs,
    unattachedBytes: 0,
    transfers: [],
    balance,
    ledger: balance > 0 ? [{ kind: 'balance_topup', delta: balance, memo: '充值', at: new Date(Date.now() - DAY_MS).toISOString() }] : [],
    orders: [],
    announcements: [],
    redemptions: [],
    usedCodes: new Set(),
  }
}
