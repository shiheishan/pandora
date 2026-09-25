/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/router 的 navigate，依赖 ../../../ui，依赖 ./AnnounceEditor，依赖 ./logic 的公告展示函数，依赖 ./queries 的 useAnnouncements，依赖 ./schemas 的类型，依赖 ./content.module.css
 * [OUTPUT]: 对外提供 AnnounceTab（内容与外观 · 公告标签）
 * [POS]: admin/screens/content 的公告（设计稿 t_announce）：左栏「＋ 新建公告」与公告卡片（置顶、级别、状态彩字、可见范围 · 时间，已撤回淡显），右栏 AnnounceEditor。选中项在地址上（rest[0] = 公告 id 或 new），没选时落在第一条；新建只是本地草稿，首次「保存草稿 / 发布」才 POST，建成后地址换成新 id。已撤回公告「复制为新公告」把标题正文与设置带进新建
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { navigate } from '../../../core/router'
import { Button, Empty, QueryView, Tag } from '../../../ui'
import { AnnounceEditor } from './AnnounceEditor'
import css from './content.module.css'
import { ANN_STATUS, annTargetLabel, annWhen, SEVERITY, type AnnForm } from './logic'
import { useAnnouncements } from './queries'
import type { Announcement } from './schemas'

const open = (id: string) => navigate(`/content/announce/${id}`)

export function AnnounceTab({ rest }: { rest: string[] }) {
  const query = useAnnouncements()
  // 「复制为新公告」的预填；每次复制换一个 seed，让新建编辑区重新挂载
  const [draft, setDraft] = useState<{ seed: number; form: AnnForm | null }>({ seed: 0, form: null })
  const selected = rest[0] ?? null

  const startNew = (form: AnnForm | null) => {
    setDraft((d) => ({ seed: d.seed + 1, form }))
    open('new')
  }

  return (
    <QueryView query={query} rows={5} empty={null}>
      {(data) => {
        const list = data.announcements
        const current: Announcement | 'new' | null = selected === 'new' ? 'new' : (list.find((a) => a.id === selected) ?? list[0] ?? null)
        return (
          <div className={css.annLayout}>
            <div className={css.list}>
              <Button variant="primary" onClick={() => startNew(null)}>
                ＋ 新建公告
              </Button>
              {current === 'new' && (
                <button type="button" className={`${css.annItem} ${css.selected}`} aria-current="true">
                  <span className={css.itemHead}>
                    <span className={`${css.itemTitle} ${css.untitled}`}>未保存的新公告</span>
                  </span>
                  <span className={css.itemMeta}>
                    <span className={css.warn}>草稿</span>
                    <span>保存后出现在列表里</span>
                  </span>
                </button>
              )}
              {list.map((a) => (
                <AnnItem key={a.id} a={a} current={current !== 'new' && current?.id === a.id} />
              ))}
              {list.length === 0 && current !== 'new' && <Empty bare title="还没有公告" description="新建一条公告，发布后门户「消息」页与首页公告栏都能看到。" />}
            </div>
            {current === 'new' ? (
              <AnnounceEditor key={`new:${draft.seed}`} data={data} original={null} prefill={draft.form} onCopy={startNew} />
            ) : current ? (
              <AnnounceEditor key={current.id} data={data} original={current} prefill={null} onCopy={startNew} />
            ) : (
              <section className={css.panel}>
                <Empty bare title="没有选中的公告" description="左侧新建一条公告开始编辑。" />
              </section>
            )}
          </div>
        )
      }}
    </QueryView>
  )
}

function AnnItem({ a, current }: { a: Announcement; current: boolean }) {
  const st = ANN_STATUS[a.status]
  const sev = SEVERITY[a.severity]
  return (
    <button
      type="button"
      className={`${css.annItem} ${current ? css.selected : ''} ${a.status === 'withdrawn' ? css.withdrawn : ''}`}
      aria-current={current || undefined}
      onClick={() => open(a.id)}
    >
      <span className={css.itemHead}>
        {a.pinned && <span className={css.pin}>置顶</span>}
        <span className={css.itemTitle}>{a.title}</span>
        {(a.severity === 'warning' || a.severity === 'critical') && <Tag tone={sev.tone}>{sev.label}</Tag>}
      </span>
      <span className={css.itemMeta}>
        <span className={css[st.tone]}>{st.label}</span>
        <span>
          {annTargetLabel(a)} · {annWhen(a)}
        </span>
      </span>
    </button>
  )
}
