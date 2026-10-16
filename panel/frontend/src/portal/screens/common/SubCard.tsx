import { href } from '../../../core/router'
import { Tag } from '../../../ui'
import { flowCss } from './Flow'
import { LinkBox } from './LinkBox'
import { day, isLow, money, resetsLeft, type Naming } from './purchase'
import css from './SubCard.module.css'
import { expiryText, isExpiredNow, leakSources, usageText } from './card-text'
import type { Subscription, SubscriptionLink } from './subscriptions'

interface SubCardActions {
  onImport: (sub: Subscription) => void
  onRename: (sub: Subscription) => void
  onRotate: (sub: Subscription) => void
}

/**
 * 我的套餐里的一份（原型 subCard）：到期、用量条、链接尾号与「复制 / 添加到 App」、App 里显示成什么、
 * 疑似泄露的红条，主按钮「续费」或「再买一份叠加」（流量低于 15% 时「流量不够」变成主按钮），底部一行「换个套餐 · 改名 · 换新链接」。
 * 已过期的那份没有「流量不够」，「换个套餐」从底部移到主按钮旁边，次一级。
 * 卡片上的按钮自带对象：从哪张卡点进去就作用在哪一份，后面不再问。
 */
export function SubCard({ sub, naming, link, actions, compact = false }: { sub: Subscription; naming: Naming; link: SubscriptionLink | undefined; actions: SubCardActions; compact?: boolean }) {
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

  const stackSub =
    sub.plan_kind === 'one_time' && sub.stackable
      ? expired
        ? '链接不变，总流量加上' + (sub.permanent ? '' : '，天数顺延')
        : '总流量加上' + (sub.permanent ? '' : '，天数顺延')
      : ''
  const stack =
    sub.plan_kind === 'one_time' && sub.stackable ? (
      <a key="stack" className={low || !renew ? flowCss.twoLinePrimary : flowCss.twoLine} href={href('/checkout', { stack: sub.id })} id={`btn-stack-${sub.id}`}>
        再买一份叠加
        <small>{stackSub}</small>
      </a>
    ) : null

  const resets = resetsLeft(sub)
  const resetScope = sub.plan_kind === 'one_time' ? '有效期内' : '本期'
  const resetLimitNote =
    resets !== null && resets !== Number.POSITIVE_INFINITY ? `，${resetScope}还能买 ${resets} 次` : ''
  const traffic =
    !expired && resets !== null && resets !== 0 ? (
      <a key="traffic" className={low || (!renew && !stack) ? flowCss.twoLinePrimary : flowCss.twoLine} href={href(`/subs/${sub.id}/traffic`)} id={`btn-traffic-${sub.id}`}>
        流量不够
        <small>
          重置 {money(sub.reset_price!)} / 次{resetLimitNote}
        </small>
      </a>
    ) : null

  // 已过期的那份：「换个套餐」和「续费，恢复使用」并列、次一级（首次点击测试 s8：底部灰字找不到）
  const change =
    expired && sub.changeable ? (
      <a key="change" className={renew || stack ? flowCss.twoLine : flowCss.twoLinePrimary} href={href(`/subs/${sub.id}/change`)} id={`btn-changepick-${sub.id}`}>
        换个套餐
        <small>换成别的，马上恢复</small>
      </a>
    ) : null

  const primaryFirst = low && traffic
  const buttons = expired
    ? [renew, stack, change]
    : primaryFirst
      ? [traffic, renew, stack]
      : [renew, stack, traffic]
  const shown = buttons.filter(Boolean)

  const noRenewNudge =
    sub.plan_kind === 'subscription' && !expired && !sub.renewable ? (
      <div className={css.nudge}>这个套餐不再续费，到期后结束，可以换个套餐。</div>
    ) : null

  return (
    <article className={css.card} id={`card-${sub.id}`} data-sub={sub.id}>
      <div className={css.head}>
        <div className={css.title}>
          <h2 className={css.name}>{titled ? sub.label || sub.plan_name : sub.plan_name}</h2>
          {titled && <Tag>{sub.label ? sub.plan_name : tail ? `····${tail}` : sub.plan_name}</Tag>}
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
          {noRenewNudge}
          {!compact && (
            <a className={css.detailLink} href={href(`/subs/${sub.id}/detail`)}>
              节点和每天用量 ›
            </a>
          )}
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
          {sub.changeable && !expired && (
            <a className={css.quiet} href={href(`/subs/${sub.id}/change`)} id={`btn-changepick-${sub.id}`}>
              换个套餐
            </a>
          )}
          {/* 上面已有「起个名字，好分清是谁的」提示条时不再重复给入口 */}
          {!(naming.multi && !sub.label) && (
            <button type="button" className={css.quiet} onClick={() => actions.onRename(sub)} id={`btn-rename-${sub.id}`}>
              {sub.label ? '改名' : '起个名字'}
            </button>
          )}
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
