/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/router 的 href / navigate，依赖 ../../../ui 的 Button / Card / Empty / Input / QueryView / useToast，依赖 ../common/traffic 的 formatDate，依赖 ../index 的 PortalScreenProps，依赖 ./api 与 ./model
 * [OUTPUT]: 默认导出 Help 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/help 的入口：帮助中心（门户-09）。地址驱动：#/help 宽屏左列目录 + 右列第一篇，#/help/<slug> 打开指定文章；< 640 目录与文章分屏，文章头有「全部文章」返回。左列搜索框 300 毫秒防抖后交给后端 q；文章底部「有帮助」反馈（每篇每版只发一次）与「仍未解决，提交工单」（直接去 #/tickets/new，不调接口）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { isApiError } from '../../../core/api'
import { href, navigate } from '../../../core/router'
import { Button, Card, Empty, Input, QueryView, useToast } from '../../../ui'
import { formatDate } from '../common/traffic'
import type { PortalScreenProps } from '../index'
import { useHelpFeedback, useHelpPage, useHelpPages, type ContentPage } from './api'
import css from './Help.module.css'
import { articleBlocks, helpGroups, SEARCH_MAX } from './model'

const SEARCH_DEBOUNCE_MS = 300

export default function Help({ rest }: PortalScreenProps) {
  const routeSlug = rest[0] ?? null
  const [input, setInput] = useState('')
  const q = useDebounced(input.trim(), SEARCH_DEBOUNCE_MS)
  const pages = useHelpPages(q)
  const groups = pages.data ? helpGroups(pages.data) : []
  // 宽屏没指定文章时右列显示目录第一篇；搜索时即第一条命中
  const slug = routeSlug ?? groups[0]?.items[0]?.slug ?? null
  // 每篇每个版本只反馈一次，切文章再回来仍记得
  const [voted, setVoted] = useState<ReadonlySet<string>>(() => new Set())

  return (
    <div className={css.layout} data-view={routeSlug ? 'article' : 'list'}>
      <div className={css.listPane}>
        <Input type="search" aria-label="搜索帮助文章" placeholder="搜索帮助文章" maxLength={SEARCH_MAX} value={input} onChange={(e) => setInput(e.target.value)} />
        <Card flush className={css.toc}>
          <QueryView
            query={pages}
            rows={4}
            isEmpty={(d) => helpGroups(d).length === 0}
            empty={
              q ? (
                <Empty bare title="没有找到相关文章" description="换个关键词试试，或直接提交工单。" action={<NewTicketButton />} />
              ) : (
                <Empty bare title="还没有帮助文章" description="遇到问题可以直接提交工单，客服会尽快回复。" action={<NewTicketButton />} />
              )
            }
          >
            {() => (
              <nav aria-label="帮助文章">
                {groups.map((g) => (
                  <section key={g.category} className={css.group}>
                    <h3 className={css.groupTitle}>{g.category}</h3>
                    <ul className={css.items}>
                      {g.items.map((p) => (
                        <li key={p.slug}>
                          <a className={css.item} href={href(`/help/${p.slug}`)} aria-current={p.slug === slug ? 'page' : undefined}>
                            {p.title}
                          </a>
                        </li>
                      ))}
                    </ul>
                  </section>
                ))}
              </nav>
            )}
          </QueryView>
        </Card>
      </div>
      <div className={css.articlePane}>
        {slug && <Article slug={slug} voted={voted} onVoted={(k) => setVoted((s) => new Set(s).add(k))} />}
      </div>
    </div>
  )
}

function NewTicketButton() {
  return (
    <Button size="sm" onClick={() => navigate('/tickets/new')}>
      提交工单
    </Button>
  )
}

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timer)
  }, [value, ms])
  return settled
}

// ---------------------------------------------------------------------------
// 文章：头部「分类 · 更新于 日期」，正文按段落与「## 」小标题渲染成纯文本节点
// ---------------------------------------------------------------------------
function Article({ slug, voted, onVoted }: { slug: string; voted: ReadonlySet<string>; onVoted: (key: string) => void }) {
  const page = useHelpPage(slug)
  const back = (
    <a className={css.back} href={href('/help')}>
      ← 全部文章
    </a>
  )
  if (isApiError(page.error, 'not_found')) {
    return (
      <Card className={css.article}>
        {back}
        <Empty bare title="这篇文章不存在或已下线" description="回到目录看看其他文章，或直接提交工单。" action={<NewTicketButton />} />
      </Card>
    )
  }
  return (
    <Card className={css.article}>
      {back}
      <QueryView query={page} rows={5} isEmpty={() => false} empty={null}>
        {(p) => <ArticleBody page={p} voted={voted.has(`${p.slug}@${p.version}`)} onVoted={() => onVoted(`${p.slug}@${p.version}`)} />}
      </QueryView>
    </Card>
  )
}

function ArticleBody({ page, voted, onVoted }: { page: ContentPage & { body: string }; voted: boolean; onVoted: () => void }) {
  const toast = useToast()
  const feedback = useHelpFeedback()
  const blocks = articleBlocks(page.body)
  const meta = [page.category?.trim() || '其他', page.published_at ? `更新于 ${formatDate(page.published_at)}` : ''].filter(Boolean).join(' · ')

  function helpful() {
    feedback.mutate(
      { slug: page.slug, version: page.version, helpful: true },
      {
        onSuccess: () => {
          onVoted()
          toast('感谢反馈')
        },
        onError: (e) => toast(isApiError(e, 'not_found') ? '这篇文章已下线' : e.message || '反馈失败，请稍后重试', 'danger'),
      },
    )
  }

  return (
    <article className={css.body}>
      <div className={css.meta}>{meta}</div>
      <h2 className={css.title}>{page.title}</h2>
      {blocks.length === 0 ? (
        page.summary ? (
          <p className={css.para}>{page.summary}</p>
        ) : null
      ) : (
        blocks.map((b, i) =>
          b.kind === 'heading' ? (
            <h3 key={i} className={css.heading}>
              {b.text}
            </h3>
          ) : (
            <p key={i} className={css.para}>
              {b.text}
            </p>
          ),
        )
      )}
      <div className={css.feedback}>
        <span>{voted ? '感谢你的反馈' : '这篇文章有帮助吗？'}</span>
        {!voted && (
          <Button size="sm" busy={feedback.isPending} onClick={helpful}>
            有帮助
          </Button>
        )}
        <Button size="sm" onClick={() => navigate('/tickets/new')}>
          仍未解决，提交工单
        </Button>
      </div>
    </article>
  )
}
