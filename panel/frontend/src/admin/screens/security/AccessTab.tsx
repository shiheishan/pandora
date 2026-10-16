import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { Empty, Input, QueryView } from '../../../ui'
import { ACCESS_PAGE, ACCESS_VIEWS, accessDetail, accessKey, accessOutcomeLabel, accessTooltip, CATEGORY_LABEL, clockTime, EMPTY_ACCESS_FILTER, isAccessError, type AccessFilter, type AccessView } from './logic'
import { useAccessLog } from './queries'
import type { AccessItem } from './schemas'
import css from './security.module.css'

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

      <section className={css.terminal} aria-label="安全事件实时尾随">
        <header className={css.termHead}>
          <span className={`${css.liveDot} ${tailing ? css.live : ''}`} aria-hidden="true" />
          <span>实时尾随 · 登录 / 注册 / 订阅拉取 / 管理动作</span>
          <span className={css.termHint}>{tailing ? '每 5 秒刷新' : offset > 0 ? '查看更早的记录时不刷新' : '已暂停'}</span>
          <span className={css.spacer} />
          <ViewSeg value={filter.view} onChange={pick} />
          {offset === 0 && (
            <button type="button" className={css.termButton} onClick={() => setPaused((v) => !v)}>
              {paused ? '继续' : '暂停'}
            </button>
          )}
        </header>
        <QueryView
          query={log}
          rows={10}
          isEmpty={(items) => items.length === 0}
          empty={
            <Empty
              bare
              className={css.termEmpty}
              title={offset > 0 ? '没有更早的记录了' : '没有符合条件的安全事件'}
              description={offset > 0 ? '回到最新继续尾随。' : '换个分段或清空筛选再看。'}
            />
          }
        >
          {(items) => (
            <div role="table" aria-label="安全事件" className={css.termRows}>
              {items.map((item, i) => (
                <AccessRow key={accessKey(item, i)} item={item} />
              ))}
            </div>
          )}
        </QueryView>
        {(offset > 0 || (log.data?.length ?? 0) >= ACCESS_PAGE) && (
          <footer className={css.termFoot}>
            <span>第 {offset / ACCESS_PAGE + 1} 页</span>
            <span className={css.spacer} />
            {offset > 0 && (
              <button type="button" className={css.termButton} onClick={() => setOffset(0)}>
                回到最新
              </button>
            )}
            <button type="button" className={css.termButton} disabled={(log.data?.length ?? 0) < ACCESS_PAGE || log.isFetching} onClick={() => setOffset(offset + ACCESS_PAGE)}>
              更早
            </button>
          </footer>
        )}
      </section>
    </div>
  )
}

/** 分段按钮组：设计稿的文字按钮样式，键盘与 FilterTabs 同一套（radiogroup + 可移动 tabindex） */
function ViewSeg({ value, onChange }: { value: AccessView; onChange: (v: AccessView) => void }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([])

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = ACCESS_VIEWS.findIndex(([v]) => v === value)
    const last = ACCESS_VIEWS.length - 1
    const next =
      event.key === 'ArrowRight' || event.key === 'ArrowDown'
        ? (current + 1) % ACCESS_VIEWS.length
        : event.key === 'ArrowLeft' || event.key === 'ArrowUp'
          ? (current - 1 + ACCESS_VIEWS.length) % ACCESS_VIEWS.length
          : event.key === 'Home'
            ? 0
            : event.key === 'End'
              ? last
              : -1

    if (next < 0) return
    event.preventDefault()
    onChange(ACCESS_VIEWS[next]![0])
    refs.current[next]?.focus()
  }

  return (
    <div role="radiogroup" aria-label="事件分段" className={css.segGroup} onKeyDown={onKeyDown}>
      {ACCESS_VIEWS.map(([v, label], i) => {
        const on = v === value
        return (
          <button
            key={v}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            className={`${css.seg} ${on ? css.segOn : ''}`}
            onClick={() => onChange(v)}
          >
            {label}
          </button>
        )
      })}
    </div>
  )
}

function AccessRow({ item }: { item: AccessItem }) {
  const bad = isAccessError(item)
  return (
    <div role="row" className={css.termRow} title={accessTooltip(item)}>
      <span role="cell" className={css.termTime}>
        {clockTime(item.occurred_at)}
      </span>
      <span role="cell" className={css.termCat}>
        {CATEGORY_LABEL[item.category]}
      </span>
      <span role="cell" className={bad ? css.termBad : css.termOk}>
        {accessOutcomeLabel(item)}
      </span>
      <span role="cell" className={css.termIp}>
        {item.ip ?? '—'}
      </span>
      <span role="cell" className={css.ellipsis}>
        {accessDetail(item)}
      </span>
    </div>
  )
}
