import { useState } from 'react'
import { href, navigate, useHashLocation } from '../../../core/router'
import { Button, Empty, FilterTabs, QueryView, Tag } from '../../../ui'
import { StatusDot } from '../../kit'
import { DebouncedInput } from './DebouncedInput'
import {
  ACCESS_PAGE,
  ACCESS_VIEWS,
  accessAccount,
  accessFromQuery,
  accessIpText,
  accessKey,
  accessOutcomeLabel,
  accessToQuery,
  accessTone,
  accessTooltip,
  accessUserFilterLabel,
  actionLabel,
  CATEGORY_LABEL,
  clockTime,
  type AccessFilter,
  type AccessTone,
  type AccessView,
} from './logic'
import { useAccessLog } from './queries'
import type { AccessItem } from './schemas'
import css from './security.module.css'

const TONE_CLASS: Record<AccessTone, string | undefined> = { ok: css.toneOk, warn: css.toneWarn, bad: css.toneBad, none: css.toneNone }

const VIEW_ITEMS = ACCESS_VIEWS.map(([value, label]) => ({ value, label }))

export function AccessTab() {
  const { filter, offset } = accessFromQuery(useHashLocation().query)
  const [paused, setPaused] = useState(false)
  const tailing = !paused && offset === 0
  const log = useAccessLog(filter, offset, tailing)

  const go = (f: AccessFilter, o: number) => navigate('/security/access', { replace: true, query: accessToQuery(f, o) })
  const pick = (view: AccessView) => go({ ...filter, view }, 0)
  const clearBoxes = () => go({ ...filter, ip: '', user: '' }, 0)
  const hasBoxes = Boolean(filter.ip.trim() || filter.user.trim())
  const full = (log.data?.length ?? 0) >= ACCESS_PAGE
  const userFilterEmail = accessUserFilterLabel(filter.user, log.data ?? [])

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <DebouncedInput
          value={filter.ip}
          onChange={(ip) => go({ ...filter, ip }, 0)}
          mono
          aria-label="按 IP 筛选"
          fieldClassName={css.prefix}
          placeholder="IP（精确匹配）"
        />
        <DebouncedInput
          value={filter.user}
          onChange={(user) => go({ ...filter, user }, 0)}
          aria-label="按账号筛选"
          fieldClassName={css.search}
          placeholder="账号邮箱片段或用户 ID（含冲着他的登录失败）"
        />
        <span className={css.spacer} />
        <span className={css.faint}>{log.data?.length ?? 0} 条</span>
        {hasBoxes && (
          <button type="button" className={css.inlineClear} onClick={clearBoxes}>
            清空筛选
          </button>
        )}
      </div>

      {userFilterEmail && (
        <p className={css.faint}>
          只看 <span className={css.strong}>{userFilterEmail}</span> 的记录（含冲着这个账号的登录失败）
        </p>
      )}

      <section className={css.box} aria-label="安全事件实时监控">
        <header className={css.boxHead}>
          {tailing ? <StatusDot tone="ok">实时监控</StatusDot> : <StatusDot tone="off">{offset > 0 ? '查看更早的记录' : '已暂停'}</StatusDot>}
          <span className={css.boxHeadSub}>登录 / 注册 / 订阅拉取 / 管理动作 · 每 5 秒刷新</span>
          <span className={css.spacer} />
          {offset === 0 && (
            <Button size="sm" onClick={() => setPaused((v) => !v)}>
              {paused ? '继续' : '暂停'}
            </Button>
          )}
        </header>
        <div className={css.boxFilter}>
          <FilterTabs label="事件分段" items={VIEW_ITEMS} value={filter.view} onChange={pick} />
        </div>
        <QueryView
          query={log}
          rows={10}
          isEmpty={(items) => items.length === 0}
          empty={
            <Empty
              bare
              className={css.empty}
              title={offset > 0 ? '没有更早的记录了' : '没有符合条件的安全事件'}
              description={offset > 0 ? '回到最新继续监控。' : '换个分段或清空筛选再看。'}
            />
          }
        >
          {(items) => (
            <div role="table" aria-label="安全事件" className={css.logs}>
              {items.map((item, i) => (
                <AccessRow key={accessKey(item, i)} item={item} />
              ))}
            </div>
          )}
        </QueryView>
        <footer className={css.boxFoot}>
          <span>第 {offset / ACCESS_PAGE + 1} 页</span>
          <span className={css.spacer} />
          <Button size="sm" disabled={offset === 0} onClick={() => go(filter, 0)}>
            回到最新
          </Button>
          <Button size="sm" disabled={!full || log.isFetching} onClick={() => go(filter, offset + ACCESS_PAGE)}>
            更早
          </Button>
        </footer>
      </section>
    </div>
  )
}

/** 一行事件（样稿 .lg）：时间、分类、结果、IP、账号 · 动作 */
function AccessRow({ item }: { item: AccessItem }) {
  const acc = accessAccount(item)
  const actionText = item.action ? actionLabel(item.action) : null
  const ipCell = accessIpText(item)

  return (
    <div role="row" className={css.logRow} title={accessTooltip(item)}>
      <span role="cell" className={css.logTime}>
        {clockTime(item.occurred_at)}
      </span>
      <span role="cell" className={css.logCat}>
        <Tag tone="neutral">{CATEGORY_LABEL[item.category]}</Tag>
      </span>
      <span role="cell" className={TONE_CLASS[accessTone(item)]}>
        {accessOutcomeLabel(item)}
      </span>
      <span role="cell" className={css.logIp} title={ipCell.title}>
        {ipCell.ip}
        {ipCell.geo && <span className={css.logGeo}> · {ipCell.geo}</span>}
      </span>
      <span role="cell" className={css.logAccount}>
        {acc.prefix && <span className={css.logAction}>{acc.prefix}</span>}
        {acc.userId ? (
          <a className={css.logLink} href={href(`/users/list/${encodeURIComponent(acc.userId)}`)}>
            {acc.label}
          </a>
        ) : (
          acc.label
        )}
        {actionText && (
          <span className={css.logAction}>
            {' · '}
            {actionText}
            {actionText !== item.action && <span className={css.logCode}>{item.action}</span>}
          </span>
        )}
      </span>
    </div>
  )
}
