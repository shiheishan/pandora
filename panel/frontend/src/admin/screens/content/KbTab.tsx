/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./KbEditor，依赖 ./logic 的知识库函数，依赖 ./queries，依赖 ./schemas，依赖 ./content.module.css
 * [OUTPUT]: 对外提供 KbTab（内容与外观 · 知识库标签）
 * [POS]: admin/screens/content 的知识库（设计稿 t_kb）：左栏按分类分组的文章目录（v{最新版本}，已归档淡显）与底部「＋ 新文章」，顶部补了类型切换与搜索（契约待补·前端）；中栏 KbEditor；右栏版本历史（v{n} · 当前、状态、作者 · 日期），点历史行在中栏只读查看那一版，可「以此版本恢复」为新版本、可单独归档仍在发布的旧版。
 *        选中文章在地址上（rest[0] = slug 或 new），没选时落在目录第一篇；列表一次取该类型全部版本，按 slug 聚合、前端搜索
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Segmented, Skeleton, TextArea, useToast } from '../../../ui'
import css from './content.module.css'
import { KbEditor } from './KbEditor'
import { dropsIntent, groupArticles, groupByCategory, kbBody, kbFormFrom, KIND_LABEL, knownCategories, PAGE_STATUS_LABEL, type Article } from './logic'
import { useContentPage, useContentPages, useFailure, useIntentKey, useInvalidateContent } from './queries'
import { KINDS, pageArchived, pageSaved, type Kind, type Page } from './schemas'

const open = (slug: string) => navigate(`/content/kb/${slug}`)
const STATUS_TONE = { draft: css.warn, published: css.ok, archived: css.muted } as const

export function KbTab({ rest }: { rest: string[] }) {
  const [kind, setKind] = useState<Kind>('kb_article')
  const [q, setQ] = useState('')
  const pages = useContentPages(kind)
  const selected = rest[0] ?? null

  return (
    <QueryView query={pages} rows={6} empty={null} isEmpty={() => false}>
      {(list) => {
        const articles = groupArticles(list)
        const groups = groupByCategory(articles, q)
        const first = groups[0]?.items[0] ?? null
        const current: Article | 'new' | null = selected === 'new' ? 'new' : (articles.find((a) => a.slug === selected) ?? first)
        return (
          <div className={css.kbLayout}>
            <nav className={css.tree} aria-label="文章目录">
              <div className={css.treeTools}>
                <Segmented size="sm" label="内容类型" value={kind} options={KINDS.map((k) => ({ value: k, label: KIND_LABEL[k] }))} onChange={setKind} />
                <Input size="sm" type="search" aria-label="搜索标题或标识" placeholder="搜索标题或标识" value={q} onChange={(e) => setQ(e.target.value)} />
              </div>
              {groups.map((g) => (
                <div key={g.cat}>
                  <div className={css.treeCat}>{g.cat}</div>
                  {g.items.map((a) => (
                    <button
                      key={a.slug}
                      type="button"
                      className={`${css.treeItem} ${current !== 'new' && current?.slug === a.slug ? css.treeCurrent : ''} ${a.archived ? css.dim : ''}`}
                      aria-current={(current !== 'new' && current?.slug === a.slug) || undefined}
                      onClick={() => open(a.slug)}
                    >
                      <span className={css.itemTitle}>{a.latest.title}</span>
                      <span className={css.treeVersion}>v{a.latest.version}</span>
                    </button>
                  ))}
                </div>
              ))}
              {groups.length === 0 && (
                <div className={css.treeEmpty}>
                  <Empty bare title={q ? '没有匹配的文章' : `还没有${KIND_LABEL[kind]}`} description={q ? '换个关键词，或清空搜索。' : '点下方「＋ 新文章」写第一篇。'} />
                </div>
              )}
              <div className={css.treeFoot}>
                <Button size="sm" className={css.dashed} onClick={() => open('new')}>
                  ＋ 新文章
                </Button>
              </div>
            </nav>
            {current === 'new' ? (
              <>
                <KbEditor key={`new:${kind}`} article={null} page={null} kind={kind} categories={knownCategories(articles)} takenSlugs={articles.map((a) => a.slug)} />
                <section className={css.history}>
                  <div className={css.historyHead}>版本历史</div>
                  <div className={css.treeEmpty}>
                    <span className={css.faint}>保存后在这里看到每个版本。</span>
                  </div>
                </section>
              </>
            ) : current ? (
              <ArticlePane key={current.slug} article={current} categories={knownCategories(articles)} />
            ) : (
              <section className={`${css.panel} ${css.spanRest}`}>
                <Empty bare title="没有选中的文章" description="左侧选一篇文章，或新建一篇。" />
              </section>
            )}
          </div>
        )
      }}
    </QueryView>
  )
}

/** 中栏与右栏：编辑最新版，或只读查看历史里的某一版 */
function ArticlePane({ article, categories }: { article: Article; categories: readonly string[] }) {
  // 记 id 不记行：写后列表重拉，状态要跟着新行走
  const [viewing, setViewing] = useState<string | null>(null)
  const latest = useContentPage(article.latest.id, article.slug)
  const shown = article.rows.find((r) => r.id === viewing && r.id !== article.latest.id) ?? null

  return (
    <>
      {shown ? (
        <VersionView article={article} row={shown} onBack={() => setViewing(null)} />
      ) : latest.data ? (
        <KbEditor article={article} page={latest.data} kind={article.latest.kind} categories={categories} takenSlugs={[]} />
      ) : (
        <section className={css.panel}>
          <QueryView query={latest} rows={6} empty={null}>
            {() => null}
          </QueryView>
        </section>
      )}
      <section className={css.history} aria-label="版本历史">
        <div className={css.historyHead}>版本历史</div>
        {article.rows.map((r) => {
          const isLatest = r.id === article.latest.id
          return (
            <button key={r.id} type="button" className={`${css.historyRow} ${(shown ? shown.id === r.id : isLatest) ? css.treeCurrent : ''}`} aria-current={(shown ? shown.id === r.id : isLatest) || undefined} onClick={() => setViewing(isLatest ? null : r.id)}>
              <span className={css.historyVersion}>
                v{r.version}
                {isLatest ? ' · 当前' : ''}
                <span className={STATUS_TONE[r.status]}>{PAGE_STATUS_LABEL[r.status]}</span>
              </span>
              <span className={css.faint}>
                {r.created_by_name ?? '未知作者'} · {formatDateTime(r.created_at).slice(5, 10)}
              </span>
            </button>
          )
        })}
      </section>
    </>
  )
}

/** 历史版本只读：以此版本内容再发布成新版本（后端「每次保存生成新版本」），仍在发布的旧版可单独归档 */
function VersionView({ article, row, onBack }: { article: Article; row: Page; onBack: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const detail = useContentPage(row.id, article.slug)
  const [archiving, setArchiving] = useState(false)
  const latestVersion = article.latest.latest_version ?? article.latest.version

  const restore = useMutation({
    mutationFn: (p: Page) => {
      const body = kbBody(kbFormFrom(p), 'published', latestVersion, p)
      return api.post('v1/content-pages', pageSaved, { body, idempotencyKey: intent.keyFor([p.id, 'restore', body]) })
    },
    onSuccess: (r) => {
      intent.reset()
      toast(`已用 v${row.version} 的内容发布为 v${r.page.version}`)
      void invalidate('pages')
      onBack()
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error)
    },
  })

  const archive = useMutation({
    mutationFn: (p: Page) => api.post(`v1/content-pages/${p.id}/archive`, pageArchived, { body: { expected_version: p.version }, idempotencyKey: intent.keyFor([p.id, 'archive', p.version]) }),
    onSuccess: () => {
      intent.reset()
      toast(`v${row.version} 已归档`)
      void invalidate('pages')
    },
    onError: (error) => {
      if (dropsIntent(error)) intent.reset()
      if (isApiError(error, 'conflict')) void invalidate('pages')
      fail(error)
    },
  })

  const busy = restore.isPending || archive.isPending
  return (
    <section className={css.panel} aria-label={`查看 v${row.version}`}>
      <div className={css.banner}>
        正在查看 v{row.version}（{PAGE_STATUS_LABEL[row.status]}，{row.created_by_name ?? '未知作者'} · {formatDateTime(row.created_at)}），只读。
      </div>
      {detail.data ? (
        <>
          <div className={css.titleRow}>
            <Input aria-label="文章标题" className={css.kbTitle} value={detail.data.title} readOnly />
            <Input aria-label="分类" className={css.kbTitle} value={detail.data.category ?? ''} placeholder="未分类" readOnly />
          </div>
          <TextArea aria-label="文章正文" mono rows={14} value={detail.data.body ?? ''} readOnly />
        </>
      ) : detail.isPending ? (
        <Skeleton height={320} />
      ) : (
        <QueryView query={detail} empty={null}>
          {() => null}
        </QueryView>
      )}
      <div className={css.editorBar}>
        <Button variant="ghost" onClick={onBack}>
          回到最新版
        </Button>
        <span className={css.spacer} />
        {row.status === 'published' && (
          <Button variant="danger" disabled={busy} busy={archive.isPending} onClick={() => setArchiving(true)}>
            归档此版本
          </Button>
        )}
        <Button variant="primary" disabled={busy || !detail.data} busy={restore.isPending} onClick={() => detail.data && restore.mutate(detail.data)}>
          以此版本恢复为 v{latestVersion + 1}
        </Button>
      </div>
      <ConfirmModal
        open={archiving}
        title={`归档 v${row.version}？`}
        body="这一版不再对门户用户展示。其他版本不受影响。"
        confirmLabel="归档"
        tone="danger"
        onConfirm={() => {
          archive.mutate(row)
          setArchiving(false)
        }}
        onCancel={() => setArchiving(false)}
      />
    </section>
  )
}
