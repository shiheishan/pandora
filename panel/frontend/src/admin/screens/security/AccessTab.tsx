import { useEffect, useState } from 'react'
import { Button, Empty, FilterTabs, Input, QueryView, Tag } from '../../../ui'
import { StatusDot } from '../../kit'
import {
  ACCESS_PAGE,
  ACCESS_VIEWS,
  accessKey,
  accessOutcomeLabel,
  accessTone,
  accessTooltip,
  accessUser,
  CATEGORY_LABEL,
  clockTime,
  EMPTY_ACCESS_FILTER,
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
  const [ip, setIp] = useState('')
  const [user, setUser] = useState('')
  const [filter, setFilter] = useState<AccessFilter>(EMPTY_ACCESS_FILTER)
  const [offset, setOffset] = useState(0)
  const [paused, setPaused] = useState(false)
  const tailing = !paused && offset === 0
  const log = useAccessLog(filter, offset, tailing)

  // 两个筛选框停手 300ms 再查
  useEffect(() => {
    const timer = setTimeout(() => {
      setFilter((f) => (f.ip === ip && f.user === user ? f : { ...f, ip, user }))
      setOffset(0)
    }, 300)
    return () => clearTimeout(timer)
  }, [ip, user])

  const pick = (view: AccessView) => {
    setFilter((f) => ({ ...f, view }))
    setOffset(0)
  }
  const clearBoxes = () => {
    setIp('')
    setUser('')
    setFilter((f) => ({ ...f, ip: '', user: '' }))
    setOffset(0)
  }
  const hasBoxes = Boolean(filter.ip.trim() || filter.user.trim())
  const full = (log.data?.length ?? 0) >= ACCESS_PAGE

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Input size="sm" mono aria-label="按 IP 筛选" fieldClassName={css.prefix} placeholder="IP（精确匹配）" value={ip} onChange={(e) => setIp(e.target.value)} />
        <Input size="sm" aria-label="按账号筛选" fieldClassName={css.search} placeholder="账号邮箱片段或用户 ID" value={user} onChange={(e) => setUser(e.target.value)} />
        <span className={css.spacer} />
        <span className={css.faint}>{log.data?.length ?? 0} 条</span>
        {hasBoxes && (
          <button type="button" className={css.inlineClear} onClick={clearBoxes}>
            清空筛选
          </button>
        )}
      </div>

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
          <Button size="sm" disabled={offset === 0} onClick={() => setOffset(0)}>
            回到最新
          </Button>
          <Button size="sm" disabled={!full || log.isFetching} onClick={() => setOffset(offset + ACCESS_PAGE)}>
            更早
          </Button>
        </footer>
      </section>
    </div>
  )
}

/** 一行事件（样稿 .lg）：时间、分类、结果、IP、账号 · 动作 */
function AccessRow({ item }: { item: AccessItem }) {
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
      <span role="cell" className={css.logIp}>
        {item.ip ?? '—'}
      </span>
      <span role="cell" className={css.logAccount}>
        {accessUser(item)}
        {item.action && <span className={css.logAction}> · {item.action}</span>}
      </span>
    </div>
  )
}
