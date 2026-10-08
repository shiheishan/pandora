import { href } from '../../../core/router'
import { Tag } from '../../../ui'
import { flowCss } from './Flow'
import { LinkBox } from './LinkBox'
import { day, gb, isLow, moneyShort, type Naming } from './purchase'
import css from './SubCard.module.css'
import { expiryText, isExpiredNow, leakSources, usageText } from './card-text'
import type { Subscription, SubscriptionLink } from './subscriptions'

export interface SubCardActions {
  onImport: (sub: Subscription) => void
  onRename: (sub: Subscription) => void
  onRotate: (sub: Subscription) => void
  /** 升级前的旧流量包挪一次（用户 10-07）；不给就不显示这一行 */
  onMove?: (sub: Subscription) => void
}

/**
 * 我的套餐里的一份（原型 subCard）：到期、用量条、链接尾号与「复制 / 添加到 App」、App 里显示成什么、
 * 疑似泄露的红条，主按钮「续费」（流量低于 15% 时「加流量」变成主按钮），底部一行「换个套餐 · 改名 · 换新链接」。
 * 卡片上的按钮自带对象：从哪张卡点进去就作用在哪一份，后面不再问。
 */
export function SubCard({
  sub,
  naming,
  link,
  minPack,
  actions,
  compact = false,
  moveTo = [],
}: {
  sub: Subscription
  naming: Naming
  link: SubscriptionLink | undefined
  minPack: number | null
  actions: SubCardActions
  compact?: boolean
  /** 旧流量包能挪去的那几份（生效中或可救回、不是这一份） */
  moveTo?: readonly Subscription[]
}) {
  const expired = isExpiredNow(sub)
  const exp = expiryText(sub)
  const usage = usageText(sub)
  const low = !expired && isLow(sub)
  const leak = leakSources(sub, link)
  const tail = naming.tail(sub)
  const titled = naming.multi || sub.label !== null

  const renew = sub.renew_until ? (
    <a key="renew" className={low ? flowCss.twoLine : flowCss.twoLinePrimary} href={href('/checkout', { renew: sub.id })} id={`btn-renew-${sub.id}`}>
      {expired ? '续费，恢复使用' : '续费'}
      <small>
        {expired ? '链接不变，' : ''}续到 {day(sub.renew_until)}
      </small>
    </a>
  ) : null
  const traffic =
    !expired && minPack !== null ? (
      <a key="traffic" className={low || !renew ? flowCss.twoLinePrimary : flowCss.twoLine} href={href(`/subs/${sub.id}/traffic`)} id={`btn-traffic-${sub.id}`}>
        加流量
        <small>{moneyShort(minPack)} 起，马上到账</small>
      </a>
    ) : null
  const buttons = low ? [traffic, renew] : [renew, traffic]
  const shown = buttons.filter(Boolean)

  return (
    <article className={css.card} id={`card-${sub.id}`} data-sub={sub.id}>
      <div className={css.head}>
        <div className={css.title}>
          <h2 className={css.name}>{titled ? sub.label || sub.plan_name : sub.plan_name}</h2>
          {titled && <Tag>{sub.label ? sub.plan_name : tail ? `····${tail}` : sub.plan_name}</Tag>}
          {sub.status === 'past_due' && <Tag tone="danger">待续费</Tag>}
          {sub.status === 'grace' && <Tag tone="warn">快到期了</Tag>}
        </div>
        <div className={css.exp} data-tone={exp.tone}>
          {exp.main}
          {exp.sub && <span className={css.expAt}>{exp.sub}</span>}
        </div>
      </div>

      {expired ? (
        <div className={css.usage}>
          <div className={css.usageTop}>
            <span>已暂停使用</span>
            <span>续费后马上恢复</span>
          </div>
        </div>
      ) : (
        <div className={css.usage} data-low={low ? '' : undefined}>
          <div className={css.usageTop}>
            <span>{usage.label}</span>
            <b>{usage.right}</b>
          </div>
          {usage.pct !== null && (
            <div className={css.bar} role="progressbar" aria-label="已用" aria-valuemin={0} aria-valuemax={100} aria-valuenow={usage.pct}>
              <i className={css.fill} style={{ width: `${usage.pct}%` }} />
            </div>
          )}
          {!compact && (
            <a className={css.detailLink} href={href(`/subs/${sub.id}/detail`)}>
              节点和每天用量 ›
            </a>
          )}
        </div>
      )}

      {!compact && actions.onMove && sub.legacy_movable_pack_bytes > 0 && moveTo.length > 0 && (
        <div className={css.move}>
          <span>升级前买的 {gb(sub.legacy_movable_pack_bytes)} 流量包现在加在这一份上，可以挪到别的一份（只能挪一次）。</span>
          <button type="button" className={flowCss.mini} onClick={() => actions.onMove?.(sub)} id={`btn-move-${sub.id}`}>
            {moveTo.length === 1 ? `挪到「${naming.dn(moveTo[0]!)}」` : '挪到别的一份'}
          </button>
        </div>
      )}

      {!compact && (
        <LinkBox sub={sub} url={link?.url} naming={naming} onImport={() => actions.onImport(sub)}>
          {expired && <div className={flowCss.linkSub}>续费后这个链接自动恢复，不用重新添加。</div>}
          {leak !== null && (
            <div className={css.leak}>
              <span>
                近 24 小时有 {leak} 个地方在用，超过 {sub.device_limit} 台的上限，可能泄露了。
              </span>
              <button type="button" className={flowCss.miniDanger} onClick={() => actions.onRotate(sub)} id={`btn-rotate-${sub.id}`}>
                换新链接
              </button>
            </div>
          )}
        </LinkBox>
      )}

      {!compact && naming.multi && !sub.label && (
        <div className={css.nudge}>
          <span>起个名字，好分清是谁的</span>
          <button type="button" className={flowCss.mini} onClick={() => actions.onRename(sub)}>
            起名字
          </button>
        </div>
      )}

      {shown.length > 0 && <div className={shown.length === 1 ? css.actionsOne : css.actions}>{shown}</div>}

      {!compact && (
        <div className={css.more}>
          {sub.changeable && (
            <a className={css.quiet} href={href(`/subs/${sub.id}/change`)} id={`btn-changepick-${sub.id}`}>
              换个套餐
            </a>
          )}
          <button type="button" className={css.quiet} onClick={() => actions.onRename(sub)} id={`btn-rename-${sub.id}`}>
            {sub.label ? '改名' : '起个名字'}
          </button>
          {!expired && leak === null && link && (
            <button type="button" className={css.quiet} onClick={() => actions.onRotate(sub)} id={`btn-rotate-${sub.id}`}>
              换新链接
            </button>
          )}
        </div>
      )}
    </article>
  )
}
