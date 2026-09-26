/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../ui，依赖 ./logic 的访问日志函数，依赖 ./queries 的 useAccessLog，依赖 ./schemas 的 AccessItem，依赖 ./security.module.css
 * [OUTPUT]: 对外提供 AccessTab（安全与运维 · 访问日志标签）
 * [POS]: admin/screens/security 的访问日志（设计稿 t_access 的深色「实时尾随」终端）。以后端为准这是安全事件流而不是 nginx 访问日志（契约）：列改为时间、分类徽标（原「方法」）、动作（原「路径」）、结果（原「状态码」，非成功标红）、IP · 归属地，耗时列删掉，账号与客户端放在行提示里。
 *        分段「全部 / 仅错误 / 管理端」加契约待补·前端的「登录 / 注册 / 订阅拉取」，另有 IP（精确匹配，走哈希）与账号（邮箱片段或用户 ID）两个筛选框，停手 300ms 再查。实时尾随 = 第一页每 5 秒轮询（审计表不在 SSE 里），可暂停；接口没有 total，翻页按「这页满没满」给「更早」，翻到更早时自动停止尾随
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { Button, Empty, Input, QueryView, Segmented } from '../../../ui'
import { ACCESS_PAGE, ACCESS_VIEWS, accessKey, accessOutcomeLabel, accessTooltip, CATEGORY_LABEL, clockTime, EMPTY_ACCESS_FILTER, ipLabel, isAccessError, type AccessFilter, type AccessView } from './logic'
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
  const filtered = filter.view !== 'all' || Boolean(filter.ip.trim() || filter.user.trim())
  const clear = () => {
    setIp('')
    setUser('')
    setFilter(EMPTY_ACCESS_FILTER)
    setOffset(0)
  }

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Input size="sm" mono aria-label="按 IP 筛选" fieldClassName={css.prefix} placeholder="IP（精确匹配）" value={ip} onChange={(e) => setIp(e.target.value)} />
        <Input size="sm" aria-label="按账号筛选" fieldClassName={css.search} placeholder="账号邮箱片段或用户 ID" value={user} onChange={(e) => setUser(e.target.value)} />
        {filtered && (
          <Button size="sm" variant="ghost" onClick={clear}>
            清空筛选
          </Button>
        )}
        <span className={css.spacer} />
        <span className={css.faint}>安全事件流，不是 HTTP 访问日志；行上悬停看账号与客户端</span>
      </div>

      <section className={css.terminal} aria-label="安全事件实时尾随">
        <header className={css.termHead}>
          <span className={`${css.liveDot} ${tailing ? css.live : ''}`} aria-hidden="true" />
          <span>实时尾随 · 登录 / 注册 / 订阅拉取 / 管理动作</span>
          <span className={css.termHint}>{tailing ? '每 5 秒刷新' : offset > 0 ? '查看更早的记录时不刷新' : '已暂停'}</span>
          <span className={css.spacer} />
          <Segmented<AccessView> label="事件分段" size="sm" value={filter.view} onChange={pick} options={ACCESS_VIEWS.map(([value, label]) => ({ value, label }))} />
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
              title={offset > 0 ? '没有更早的记录了' : filtered ? '没有符合条件的安全事件' : '还没有安全事件'}
              description={offset > 0 ? '回到最新继续尾随。' : filtered ? '换个分段或清空筛选再看。' : '有人登录、注册或拉取订阅后，这里会实时出现。'}
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
      <span role="cell" className={css.ellipsis}>
        {item.action ?? '—'}
      </span>
      <span role="cell" className={bad ? css.termBad : css.termOk}>
        {accessOutcomeLabel(item)}
      </span>
      <span role="cell" className={`${css.termIp} ${css.ellipsis}`}>
        {ipLabel(item)}
      </span>
    </div>
  )
}
