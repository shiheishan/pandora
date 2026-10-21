import { href } from '../../../core/router'
import { useToast } from '../../../ui'
import {
  Bar,
  BigNumber,
  Btn,
  Chip,
  Eyebrow,
  Hero,
  Meta,
  Row,
  Rows,
  SectionTitle,
  Sticker,
} from '../../kit'
import {
  changeHint,
  eyebrowText,
  goneText,
  heroTraffic,
  isExpiredNow,
  leakSources,
  lowTip,
  metaLine,
  nextUsageText,
  noRenewNote,
  pillText,
  expiryText,
  renewText,
  resetRow,
  titleOf,
} from '../common/card-text'
import { copyText } from '../common/clients'
import { isLow, type Naming } from '../common/purchase'
import type { Subscription, SubscriptionLink } from '../common/subscriptions'
import css from './Copy.module.css'

export interface CopyActions {
  onImport: (sub: Subscription) => void
  onRename: (sub: Subscription) => void
  onRotate: (sub: Subscription) => void
}

export function Copy(props: {
  sub: Subscription
  naming: Naming
  link: SubscriptionLink | undefined
  /** 站点名（眉标用） */
  site: string
  actions: CopyActions
  /** 多份时只显示选中的那份：其余的整块 hidden（浏览器测试靠 hidden 判断看不见） */
  hidden?: boolean
  /** 多份时由切换条控制：给了就加 role="tabpanel" aria-labelledby={tabId} */
  tabId?: string
}) {
  const { sub, naming, link, site, actions, hidden, tabId } = props
  const toast = useToast()
  const now = new Date()

  const expired = isExpiredNow(sub, now)
  const leak = leakSources(sub, link)
  const reset = resetRow(sub, now)
  const renewBtnText = renewText(sub, now)
  const tail = naming.tail(sub)
  const showNameNudge = naming.multi && !sub.label

  async function copy() {
    if (!link?.url) return
    const ok = await copyText(link.url)
    toast(
      ok ? `已复制「${naming.sn(sub)}」的链接（····${tail}）` : '复制失败，请稍后再试',
      ok ? 'ok' : 'danger',
    )
  }

  return (
    <article
      className={css.copy}
      id={`copy-${sub.id}`}
      data-sub={sub.id}
      hidden={hidden}
      {...(tabId ? { role: 'tabpanel', 'aria-labelledby': tabId } : {})}
    >
      {!expired ? (
        /* A. 生效中的那份 */
        <Hero
          tone="live"
          pill={pillText(sub, now)}
          pillTone={expiryText(sub, now).tone === 'ok' ? 'plain' : 'bad'}
        >
          <Eyebrow>{eyebrowText(site, sub)}</Eyebrow>
          <div className={css.titleRow}>
            <Sticker as="h2">{titleOf(sub)}</Sticker>
            {sub.is_trial && <Chip className={css.trial}>试用</Chip>}
          </div>
          {(() => {
            const t = heroTraffic(sub)
            return (
              <>
                {t.big ? (
                  <BigNumber className={css.big} value={t.big.value} unit={t.big.unit} />
                ) : (
                  <BigNumber className={css.big} value="不限" unit="流量" />
                )}
                <Meta className={css.scope}>
                  {t.total !== null ? (
                    <>
                      {t.scope} · 共 <b>{t.total}</b>
                      {t.bonus !== null && (
                        <>
                          ，含送的 <b>{t.bonus}</b>
                        </>
                      )}
                      {t.bonusNote !== '' && <>，{t.bonusNote}</>}
                      {t.tail !== '' && <>，{t.tail}</>}
                    </>
                  ) : sub.plan_kind === 'subscription' ? (
                    '每期不限流量'
                  ) : (
                    '不限流量，不清零'
                  )}
                </Meta>
                {t.ratio !== null && (
                  <Bar value={t.ratio} low={isLow(sub)} label="剩余流量" />
                )}
              </>
            )
          })()}
          <Meta size="sm">{metaLine(sub, now)}</Meta>
          {(() => {
            const next = nextUsageText(sub)
            return next ? <Meta size="sm" className={css.nextUsage}>{next}</Meta> : null
          })()}
          {(() => {
            const note = noRenewNote(sub, now)
            return note ? <p className={css.note}>{note}</p> : null
          })()}
          {(() => {
            const tip = lowTip(sub, now)
            if (!tip) return null
            return (
              <div className={css.lowTip} role="status">
                <span className={css.tipText}>
                  <b>{tip.title}</b>
                  {tip.text}
                </span>
                {reset?.open && (
                  <Btn tone="soft" size="sm" href={href('/checkout', { reset: sub.id })}>
                    重置 · {reset.price}
                  </Btn>
                )}
                {sub.changeable && (
                  <a className={css.tipGo} href={href(`/subs/${sub.id}/change`)}>
                    换更大的 ›
                  </a>
                )}
              </div>
            )
          })()}
          {leak !== null && (
            <div className={css.leak}>
              <span>
                近 24 小时有 {leak} 个地方在用，超过 {sub.device_limit} 台的上限，可能泄露了。
              </span>
              <Btn tone="danger" size="sm" onClick={() => actions.onRotate(sub)}>
                换新链接
              </Btn>
            </div>
          )}
          {showNameNudge && (
            <div className={css.nudge}>
              <span>起个名字，好分清是谁的</span>
              <Btn tone="soft" size="sm" onClick={() => actions.onRename(sub)}>
                起名字
              </Btn>
            </div>
          )}
          <div className={css.heroFoot}>
            <Btn tone="hot" grow disabled={!link?.url} onClick={() => actions.onImport(sub)}>
              添加到 App
            </Btn>
            {renewBtnText && (
              <Btn tone="soft" href={href('/checkout', { renew: sub.id })}>
                {renewBtnText}
              </Btn>
            )}
            {sub.plan_kind === 'one_time' && sub.stackable && (
              <Btn tone="soft" href={href('/checkout', { stack: sub.id })}>
                再买同款，叠加上去
              </Btn>
            )}
          </div>
        </Hero>
      ) : (
        /* B. 已到期的那份 */
        <Hero tone="off" pill="已到期" pillTone="bad">
          <Eyebrow>{eyebrowText(site, sub)}</Eyebrow>
          <div className={css.titleRow}>
            <Sticker as="h2">{titleOf(sub)}</Sticker>
            {sub.is_trial && <Chip className={css.trial}>试用</Chip>}
          </div>
          <div className={css.gone}>{goneText(sub, now)}</div>
          <Meta className={css.scope}>
            节点已经停了。续费后链接不变，App 里更新一次就能接着用。
          </Meta>
          {showNameNudge && (
            <div className={css.nudge}>
              <span>起个名字，好分清是谁的</span>
              <Btn tone="soft" size="sm" onClick={() => actions.onRename(sub)}>
                起名字
              </Btn>
            </div>
          )}
          <div className={css.heroFoot}>
            {renewBtnText && (
              <Btn tone="hot" grow href={href('/checkout', { renew: sub.id })}>
                {renewBtnText}
              </Btn>
            )}
            {sub.plan_kind === 'one_time' && sub.stackable && (
              <Btn
                tone={renewBtnText ? 'soft' : 'hot'}
                grow={!renewBtnText}
                href={href('/checkout', { stack: sub.id })}
              >
                再买同款，叠加上去
              </Btn>
            )}
            {sub.changeable && (
              <Btn
                tone="soft"
                href={href(`/subs/${sub.id}/change`)}
                className={css.twoLine}
              >
                <span>换个套餐</span>
                <small>换成别的，马上恢复</small>
              </Btn>
            )}
            <Btn tone="soft" href={href('/subs/new')}>
              另买一份
            </Btn>
          </div>
        </Hero>
      )}

      {/* 这一份操作行 */}
      <SectionTitle as="h3" className={css.titleGap}>
        这一份
      </SectionTitle>
      <Rows>
        {!expired ? (
          <>
            <Row
              icon="link"
              title="链接"
              sub={`App 里显示为 ${sub.client_name}`}
              value={link?.url ? <code>····{tail}</code> : '还没有链接'}
              end={
                link?.url ? (
                  <button type="button" className={css.copyBtn} onClick={() => void copy()}>
                    复制
                  </button>
                ) : undefined
              }
            />
            <Row
              icon="chart"
              title="节点和每天用量"
              href={href(`/subs/${sub.id}/detail`)}
            />
            {reset !== null &&
              (reset.open ? (
                <Row
                  icon="reset"
                  title={reset.title}
                  sub={reset.sub}
                  value={reset.price}
                  valueTone="num"
                  href={href('/checkout', { reset: sub.id })}
                />
              ) : (
                <Row
                  icon="reset"
                  title={reset.title}
                  sub={reset.sub}
                  value={reset.price}
                  valueTone="num"
                  chevron={false}
                />
              ))}
            {sub.changeable && (
              <Row
                icon="swap"
                title="换个套餐"
                sub={changeHint(sub)}
                href={href(`/subs/${sub.id}/change`)}
              />
            )}
            {!showNameNudge && (
              <Row
                icon="edit"
                title={sub.label ? '改名' : '起个名字'}
                onClick={() => actions.onRename(sub)}
              />
            )}
            {link?.url && leak === null && (
              <Row
                icon="renew"
                title="换新链接"
                sub="旧链接立刻失效，App 里要重新添加"
                onClick={() => actions.onRotate(sub)}
              />
            )}
          </>
        ) : (
          <>
            <Row
              icon="link"
              title="链接"
              sub="续费后照旧能用，不用重新添加"
              value={link?.url ? <code>····{tail}</code> : '还没有链接'}
              end={
                link?.url ? (
                  <button type="button" className={css.copyBtn} onClick={() => void copy()}>
                    复制
                  </button>
                ) : undefined
              }
            />
            <Row
              icon="chart"
              title="节点和每天用量"
              sub="能看到期前的记录"
              href={href(`/subs/${sub.id}/detail`)}
            />
            {!showNameNudge && (
              <Row
                icon="edit"
                title={sub.label ? '改名' : '起个名字'}
                onClick={() => actions.onRename(sub)}
              />
            )}
          </>
        )}
      </Rows>
      {expired && sub.reset_price !== null && (
        <p className={css.aside}>重置流量要在用着的套餐上办，先续费就能接着用。</p>
      )}
    </article>
  )
}
