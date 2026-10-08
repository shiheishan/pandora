import type { OrderDetail } from '../common/orders'
import { day, gb, leftOf, money, planTraffic, type Naming } from '../common/purchase'
import type { Plan } from '../common/catalog'
import type { Subscription } from '../common/subscriptions'

// ---------------------------------------------------------------------------
// 完成页的文字（原型 order().commit 与 scrResult）：付完（含收银台回跳）后按订单与地址里带回的
// 上下文重建。上下文放在地址里，因为去收银台会离开页面：
//   k=renew|change|new|pack  sub=<那一份>  was=<原到期>  old=<原套餐名>  refund=<退回余额>
//   gb=<流量包字节>  waived=<免掉的差价>  revive=1（过期后恢复）
// ---------------------------------------------------------------------------
export interface DoneContext {
  kind: 'renew' | 'change' | 'new' | 'pack'
  subId: string | null
  was: string | null
  oldPlan: string | null
  refund: number
  packBytes: number
  waived: number
  revive: boolean
}

export function doneQuery(c: DoneContext): Record<string, string> {
  const q: Record<string, string> = { k: c.kind }
  if (c.subId) q.sub = c.subId
  if (c.was) q.was = c.was
  if (c.oldPlan) q.old = c.oldPlan
  if (c.refund > 0) q.refund = String(c.refund)
  if (c.packBytes > 0) q.gb = String(c.packBytes)
  if (c.waived > 0) q.waived = String(c.waived)
  if (c.revive) q.revive = '1'
  return q
}

export function readDone(query: URLSearchParams): DoneContext {
  const k = query.get('k')
  const n = (key: string) => Math.max(0, Number(query.get(key)) || 0)
  return {
    kind: k === 'change' || k === 'new' || k === 'pack' ? k : 'renew',
    subId: query.get('sub'),
    was: query.get('was'),
    oldPlan: query.get('old'),
    refund: n('refund'),
    packBytes: n('gb'),
    waived: n('waived'),
    revive: query.get('revive') === '1',
  }
}

const METHOD_NAMES: Readonly<Record<string, string>> = { alipay: '支付宝', wxpay: '微信支付', wechat: '微信支付', qqpay: 'QQ 钱包' }

/** 「余额付了 ¥8.50，支付宝付了 ¥21.50」/「这次没有花钱」 */
export function paidText(o: Pick<OrderDetail, 'balance_applied' | 'paid_amount' | 'payments'>, waived = 0): string {
  const parts: string[] = []
  if (o.balance_applied > 0) parts.push(`余额付了 ${money(o.balance_applied)}`)
  if (o.paid_amount > 0) {
    const p = o.payments[0]
    const how = (p?.method && METHOD_NAMES[p.method]) || p?.provider_name || '在线'
    parts.push(`${how}付了 ${money(o.paid_amount)}`)
  }
  if (waived > 0) parts.push(`差价 ${money(waived)} 不到支付最低额，这次免了`)
  return parts.length ? parts.join('，') : '这次没有花钱'
}

/**
 * 新买的那一份：订单详情没有订阅 id，按套餐名与订单上的「有效期至」认；
 * 认不出时取同套餐里最新开始的那份。
 */
export function findNewSub(order: Pick<OrderDetail, 'plan_name' | 'subscription_period_end'>, held: readonly Subscription[]): Subscription | undefined {
  const same = held.filter((s) => s.plan_name === order.plan_name)
  const exact = order.subscription_period_end ? same.find((s) => s.current_period_end === order.subscription_period_end) : undefined
  const start = (s: Subscription) => (s.current_period_start ? new Date(s.current_period_start).getTime() : 0)
  return exact ?? [...same].sort((a, b) => start(b) - start(a))[0]
}

export interface DoneLines {
  title: string
  happened: string[]
  kept: string[]
  changeable?: string
  /** 换套餐：下一步在 App 里点一次更新 */
  updateHint?: string
  /** 新买：下一步是新链接 + 添加到 App */
  showNewLink: boolean
}

export function doneLines(c: DoneContext, order: OrderDetail, sub: Subscription | undefined, plan: Plan | undefined, held: readonly Subscription[], naming: Naming, balance: number | null): DoneLines {
  const paid = paidText(order, c.waived)
  const others = held.filter((s) => s.id !== sub?.id)
  const othersFine = others.length ? [`${others.map((x) => `「${naming.sn(x)}」`).join('、')}不受影响`] : []
  const end = sub?.current_period_end ? day(sub.current_period_end) : order.subscription_period_end ? day(order.subscription_period_end) : ''
  switch (c.kind) {
    case 'renew':
      return {
        title: c.revive ? '已恢复使用' : '续费好了',
        happened: [`${sub ? naming.who(sub) : '这一份'}用到 ${end}${c.was ? `（原来 ${day(c.was)}${c.revive ? '，已过期' : ''}）` : ''}`, paid],
        kept: ['链接没变，现在用的设备不用重新添加'],
        showNewLink: false,
      }
    case 'change': {
      const np = order.plan_name ?? sub?.plan_name ?? ''
      return {
        title: `已换成${np}`,
        happened: [
          `${sub && naming.multi ? `「${naming.sn(sub)}」` : '你的套餐'}已换成${np}${c.was ? `，到期日 ${day(c.was)} → ${end}` : `，用到 ${end}`}`,
          `${plan ? planTraffic(plan) : '流量'}，从 0 开始算${sub?.device_limit != null ? `；最多 ${sub.device_limit} 台同时用` : ''}`,
          c.refund > 0 ? `${money(c.refund)} 已退到余额${balance !== null ? `，现在余额 ${money(balance)}` : ''}（在「钱包」里，可用于续费、加流量、买套餐）` : paid,
        ],
        kept: ['链接没变', ...othersFine],
        updateHint: `在 App 里点一次「更新」，就能看到${np}的节点。不用重新添加。`,
        changeable: '想换回来随时可以，在卡片上点「换个套餐」。',
        showNewLink: false,
      }
    }
    case 'new':
      return {
        title: '新的一份买好了',
        happened: [`${sub ? naming.dn(sub) : (order.plan_name ?? '')}，用到 ${end}`, paid],
        kept: others.length ? [`${others.length === 1 ? `你的${others[0]!.plan_name}` : `原来的 ${others.length} 份`}照常用，链接没变`] : [],
        changeable: sub?.label ? '名字随时能改。' : '可以在「我的套餐」里给它起个名字，好分清。',
        showNewLink: true,
      }
    default: {
      const left = sub ? leftOf(sub).left : null
      return {
        title: `已加 ${gb(c.packBytes)}`,
        happened: [`${sub ? naming.who(sub) : '这一份'}${left !== null ? `这期还能用 ${gb(left)}` : '加上了'}`, paid],
        kept: ['链接没变，不用重新添加', ...(end ? [`到期日不变（${end}）`] : [])],
        showNewLink: false,
      }
    }
  }
}
