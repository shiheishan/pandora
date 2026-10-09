import { payMethodName, type OrderDetail } from '../common/orders'
import { day, gb, leftOf, money, planTraffic, type Naming } from '../common/purchase'
import type { Plan } from '../common/catalog'
import type { Subscription } from '../common/subscriptions'

// ---------------------------------------------------------------------------
// 完成页的文字（原型 order().commit 与 scrResult）：付完（含收银台回跳）后按订单与地址里带回的
// 上下文重建。上下文放在地址里，因为去收银台会离开页面：
//   k=renew|change|new|pack  sub=<那一份>  was=<原到期>  old=<原套餐名>  refund=<退回余额>
//   gb=<流量包字节>  waived=<免掉的差价>  revive=1（过期后恢复：续费或换套餐）
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

/**
 * 「余额付了 ¥29.00，支付宝付了 ¥1.00」/「这次没有花钱」：按各来源实际付了多少写——余额是订单上用掉的余额，
 * 在线那部分是渠道实收（payments 合计，不是订单总额）；渠道按付款方式的叫法说（支付宝、微信支付），不说商户名
 */
export function paidText(o: Pick<OrderDetail, 'balance_applied' | 'payments'>, waived = 0): string {
  const parts: string[] = []
  if (o.balance_applied > 0) parts.push(`余额付了 ${money(o.balance_applied)}`)
  const online = o.payments.reduce((n, p) => n + p.amount, 0)
  if (online > 0) parts.push(`${payMethodName(o.payments)}付了 ${money(online)}`)
  if (waived > 0) parts.push(`零头 ${money(waived)} 已免`)
  return parts.length ? parts.join('，') : '这次没有花钱'
}

/**
 * 这单落到的那一份：订单详情履约后带 subscription_id（新购是新开的那份，续费、换套餐、流量包是原来那份），
 * 按它认；还没履约（收银台刚回跳）时续费、换套餐、流量包先用地址里带回的那份，新购等它履约。
 */
export function doneSub(order: Pick<OrderDetail, 'subscription_id'>, ctx: Pick<DoneContext, 'kind' | 'subId'>, held: readonly Subscription[]): Subscription | undefined {
  const id = order.subscription_id ?? (ctx.kind === 'new' ? null : ctx.subId)
  return id ? held.find((s) => s.id === id) : undefined
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
  // 用了余额就顺带写还剩多少（首次点击测试：余额付完不知道还剩几块）
  const paid = paidText(order, c.waived) + (order.balance_applied > 0 && balance !== null ? `；余额还剩 ${money(balance)}` : '')
  const others = held.filter((s) => s.id !== sub?.id)
  const othersFine = others.length ? [`${others.map((x) => `「${naming.sn(x)}」`).join('、')}不受影响`] : []
  // 到期日先取订单详情回的（完成页轮询的就是它，履约后的新值）；订阅列表在下单后才重新拉取，先渲染时还是旧的
  const endAt = order.subscription_period_end ?? sub?.current_period_end
  const end = endAt ? day(endAt) : ''
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
        // 过期那份换套餐（「换成别的，马上恢复」）：先说恢复了
        title: c.revive ? `已恢复使用，换成${np}` : `已换成${np}`,
        happened: [
          `${sub && naming.multi ? `「${naming.sn(sub)}」` : '你的套餐'}已换成${np}${c.was ? `，到期日 ${day(c.was)} → ${end}` : `，用到 ${end}`}`,
          `${plan ? planTraffic(plan) : '流量'}，从 0 开始算${sub?.device_limit != null ? `；最多 ${sub.device_limit} 台同时用` : ''}`,
          c.refund > 0 ? `${money(c.refund)} 已退到余额${balance !== null ? `，现在余额 ${money(balance)}` : ''}（在「钱包」里，可用于续费、加流量、买套餐）` : paid,
        ],
        kept: ['链接没变', ...othersFine],
        updateHint: `在 App 里点一次「更新」（找到「${sub?.client_name ?? '这个配置'}」，点刷新或更新），就能看到${np}的节点。不用重新添加。`,
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
        happened: [`${sub ? naming.who(sub) : '这一份'}${left !== null ? `现在一共还能用 ${gb(left)}` : '加上了'}（流量包不过期，这期的用完再用它，用完为止）`, paid],
        kept: ['链接没变，不用重新添加', ...(end ? [`到期日不变（${end}）`] : [])],
        showNewLink: false,
      }
    }
  }
}
