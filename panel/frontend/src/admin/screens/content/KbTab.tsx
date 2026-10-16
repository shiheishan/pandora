import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { navigate, useHashLocation } from '../../../core/router'
import { Button, Empty, FilterTabs, Modal, QueryView, Table, Tag, Toolbar, SearchField, IconClose } from '../../../ui'
import { KbEditor } from './KbEditor'
import css from './kb.module.css'
import { articleRows, groupArticles, KIND_LABEL, knownCategories, PAGE_STATUS_LABEL, platformsLabel, type Article } from './logic'
import { useContentPage, useContentPages } from './queries'
import { KINDS, type Kind } from './schemas'

const LIST_PATH = '/content/kb'

const STATUS_TONE = { draft: 'warn', published: 'ok', archived: 'neutral' } as const

function isKind(v: string | null): v is Kind {
  return v !== null && (KINDS as readonly string[]).includes(v)
}

/** 列表地址上的 k / q：默认类型不写 k */
function listQuery(kind: Kind, q: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  if (kind !== 'kb_article') out.k = kind
  const trimmed = q.trim()
  if (trimmed) out.q = trimmed
  return Object.keys(out).length > 0 ? out : undefined
}

export function KbTab({ rest }: { rest: string[] }) {
  const { query: urlQuery } = useHashLocation()
  const rawKind = urlQuery.get('k')
  const kind: Kind = isKind(rawKind) ? rawKind : 'kb_article'
  const q = urlQuery.get('q') ?? ''
  const pages = useContentPages(kind)
  const selected = rest[0] ?? null

  const setList = useCallback(
    (next: { kind?: Kind; q?: string }) => {
      const k = next.kind ?? kind
      const text = next.q ?? q
      const query = listQuery(k, text)
      // 换类型回到列表（开着的文章不属于新类型）；只改搜索时保留开着的弹窗
      const keep = selected && next.kind === undefined
      navigate(keep ? `${LIST_PATH}/${encodeURIComponent(selected)}` : LIST_PATH, { replace: true, query })
    },
    [kind, q, selected],
  )

  const open = (slug: string) => navigate(`${LIST_PATH}/${encodeURIComponent(slug)}`, { query: listQuery(kind, q) })
  const close = () => navigate(LIST_PATH, { replace: true, query: listQuery(kind, q) })
  const startNew = () => open('new')

  return (
    <QueryView query={pages} rows={6} empty={null} isEmpty={() => false}>
      {(list) => {
        const articles = groupArticles(list)
        const rows = articleRows(articles, q)
        const current: Article | 'new' | null = selected === 'new' ? 'new' : selected ? (articles.find((a) => a.slug === selected) ?? null) : null
        return (
          <div className={css.page}>
            <Toolbar
              end={
                <Button variant="primary" onClick={startNew}>
                  新建文章
                </Button>
              }
            >
              <SearchBox value={q} onChange={(text) => setList({ q: text })} />
              <FilterTabs
                label="按类型筛选"
                value={kind}
                items={KINDS.map((k) => ({ value: k, label: KIND_LABEL[k] }))}
                onChange={(k) => setList({ kind: k })}
              />
            </Toolbar>
            <Table
              label="文章列表"
              rows={rows}
              rowKey={(a) => a.slug}
              onRowClick={(a) => open(a.slug)}
              columns={[
                {
                  key: 'title',
                  header: '标题',
                  render: (a) =>
                    dim(
                      a,
                      <span className={css.titleCell}>
                        <span className={css.titleMain}>{a.latest.title}</span>
                        <span className={css.titleSlug}>{a.slug}</span>
                      </span>,
                    ),
                },
                {
                  key: 'category',
                  header: '分类',
                  width: '112px',
                  render: (a) => dim(a, <span className={css.cat}>{a.latest.category?.trim() || '未分类'}</span>),
                },
                {
                  key: 'platforms',
                  header: '平台',
                  width: '140px',
                  render: (a) => dim(a, <span className={css.plats}>{platformsLabel(a.latest.target_platforms)}</span>),
                },
                {
                  key: 'status',
                  header: '状态',
                  width: '72px',
                  render: (a) => dim(a, <Tag tone={STATUS_TONE[a.latest.status]}>{PAGE_STATUS_LABEL[a.latest.status]}</Tag>),
                },
                {
                  key: 'version',
                  header: '版本',
                  width: '56px',
                  align: 'right',
                  render: (a) => dim(a, <span className={css.ver}>v{a.latest.version}</span>),
                },
              ]}
              empty={
                articles.length === 0 ? (
                  <Empty bare title={`还没有${KIND_LABEL[kind]}`} description="点「新建文章」写第一篇。" />
                ) : q.trim() ? (
                  <Empty bare title="没有匹配的文章" description="换个关键词，或清空搜索。" />
                ) : (
                  <Empty bare title={`还没有${KIND_LABEL[kind]}`} />
                )
              }
            />
            {selected === null ? null : current === 'new' ? (
              <KbEditor key={`new:${kind}`} article={null} page={null} kind={kind} categories={knownCategories(articles)} takenSlugs={articles.map((a) => a.slug)} onClose={close} />
            ) : current ? (
              <KbArticleModal key={current.slug} article={current} categories={knownCategories(articles)} onClose={close} />
            ) : (
              <Modal
                open
                size={720}
                title="文章"
                onClose={close}
                headExtra={
                  <Button variant="ghost" size="sm" aria-label="关闭" onClick={close}>
                    <IconClose />
                  </Button>
                }
              >
                <Empty bare title="文章不存在或已删除" />
              </Modal>
            )}
          </div>
        )
      }}
    </QueryView>
  )
}

/** 已有文章：先拉最新版全文再进编辑弹窗 */
function KbArticleModal({ article, categories, onClose }: { article: Article; categories: readonly string[]; onClose: () => void }) {
  const latest = useContentPage(article.latest.id, article.slug)
  if (latest.data) {
    return <KbEditor article={article} page={latest.data} kind={article.latest.kind} categories={categories} takenSlugs={[]} onClose={onClose} />
  }
  return (
    <Modal
      open
      size={720}
      divided
      title="编辑文章"
      onClose={onClose}
      headExtra={
        <Button variant="ghost" size="sm" aria-label="关闭" onClick={onClose}>
          <IconClose />
        </Button>
      }
    >
      <QueryView query={latest} rows={6} empty={null}>
        {() => null}
      </QueryView>
    </Modal>
  )
}

/** 搜索框：输入 300ms 后才回写地址 */
function SearchBox({ value, onChange }: { value: string; onChange: (q: string) => void }) {
  const [text, setText] = useState(value)
  const [synced, setSynced] = useState(value)
  if (value !== synced) {
    setSynced(value)
    setText(value)
  }
  useEffect(() => {
    if (text === value) return
    const timer = setTimeout(() => onChange(text), 300)
    return () => clearTimeout(timer)
  }, [text, value, onChange])
  return <SearchField value={text} onChange={(e) => setText(e.target.value)} placeholder="标题或标识" aria-label="搜索文章" width={240} />
}

function dim(a: Article, node: ReactNode): ReactNode {
  return a.archived ? <span className={css.dim}>{node}</span> : node
}
