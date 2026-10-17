import { useEffect, useState } from 'react'
import { isApiError } from '../../../core/api'
import { href, useHashLocation } from '../../../core/router'
import { Empty, QueryView, useToast } from '../../../ui'
import { Btn, Icon } from '../../kit'
import { parseGuideDevice, thisDevice, UPDATE_GUIDE_PARAM } from '../common/app-update'
import { formatDate } from '../common/traffic'
import type { PortalScreenProps } from '../index'
import { useHelpFeedback, useHelpPage, useHelpPages, type ContentPage } from './api'
import css from './Help.module.css'
import { articleBlocks, helpGroups, SEARCH_MAX } from './model'
import { UpdateGuide, UpdateGuideToc } from './UpdateGuide'

const SEARCH_DEBOUNCE_MS = 300

export default function Help({ rest }: PortalScreenProps) {
  const routeSlug = rest[0] ?? null
  // 内置的「在 App 里点一次更新」：#/help?update=<设备>（走查询参数，不占后台文章 slug 的路径）
  const { query } = useHashLocation()
  const guideRaw = routeSlug === null ? query.get(UPDATE_GUIDE_PARAM) : null
  const guide = guideRaw === null ? null : parseGuideDevice(guideRaw, thisDevice())
  const [input, setInput] = useState('')
  const q = useDebounced(input.trim(), SEARCH_DEBOUNCE_MS)
  const pages = useHelpPages(q)
  const groups = pages.data ? helpGroups(pages.data) : []
  // 宽屏没指定文章时右列显示目录第一篇；搜索时即第一条命中
  const slug = guide ? null : (routeSlug ?? groups[0]?.items[0]?.slug ?? null)
  // 目录一篇都没有（搜不到，或后台还没有文章）：目录卡里已经给了「提交工单」，底部那条藏起
  const noResults = pages.data !== undefined && groups.length === 0
  // 每篇每个版本只反馈一次，切文章再回来仍记得
  const [voted, setVoted] = useState<ReadonlySet<string>>(() => new Set())

  return (
    <div className={css.hp} data-view={routeSlug || guide ? 'article' : 'list'}>
      <div className={css.side}>
        <label className={css.search}>
          <Icon name="search" size={18} className={css.searchIcon} />
          <input
            type="search"
            aria-label="搜索帮助文章"
            placeholder="搜索帮助文章"
            maxLength={SEARCH_MAX}
            value={input}
            onChange={(e) => setInput(e.target.value)}
          />
        </label>
        <UpdateGuideToc current={guide} />
        <div className={css.toc}>
          <QueryView
            query={pages}
            rows={4}
            isEmpty={(d) => helpGroups(d).length === 0}
            empty={
              q ? (
                <div className={css.empty}>
                  <b>没有找到相关文章</b>
                  换个关键词试试，或直接提交工单。
                  <Btn size="sm" href={href('/tickets/new')}>
                    提交工单
                  </Btn>
                </div>
              ) : (
                <div className={css.empty}>
                  <b>还没有帮助文章</b>
                  遇到问题可以直接提交工单，客服会尽快回复。
                  <Btn size="sm" href={href('/tickets/new')}>
                    提交工单
                  </Btn>
                </div>
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
        </div>
        {!noResults && (
          <div className={css.ask}>
            <Icon name="chat" size={18} />
            没找到答案？直接问客服
            <a href={href('/tickets/new')}>提交工单 ›</a>
          </div>
        )}
      </div>
      <div className={css.main}>
        {guide ? <UpdateGuide device={guide} /> : slug && <Article slug={slug} voted={voted} onVoted={(k) => setVoted((s) => new Set(s).add(k))} />}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 文章卡：页面上唯一的 <article>（浏览器测试靠它数卡片）；「‹ 全部文章」只在手机显示
// ---------------------------------------------------------------------------
function Article({ slug, voted, onVoted }: { slug: string; voted: ReadonlySet<string>; onVoted: (key: string) => void }) {
  const page = useHelpPage(slug)
  const back = (
    <a className={css.back} href={href('/help')}>
      ‹ 全部文章
    </a>
  )
  if (isApiError(page.error, 'not_found')) {
    return (
      <article className={css.ha}>
        {back}
        <Empty bare title="这篇文章不存在或已下线" description="回到目录看看其他文章，或直接提交工单。" action={<NewTicketButton />} />
      </article>
    )
  }
  return (
    <article className={css.ha}>
      {back}
      <QueryView query={page} rows={5} isEmpty={() => false} empty={null}>
        {(p) => <ArticleBody page={p} voted={voted.has(`${p.slug}@${p.version}`)} onVoted={() => onVoted(`${p.slug}@${p.version}`)} />}
      </QueryView>
    </article>
  )
}

function NewTicketButton() {
  return (
    <Btn size="sm" href={href('/tickets/new')}>
      提交工单
    </Btn>
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

  // 外层已经是 <article>，这里只画正文；正文为空时退回 summary
  return (
    <>
      <div className={css.meta}>{meta}</div>
      <h2 className={css.title}>{page.title}</h2>
      {blocks.length === 0 ? (
        page.summary ? (
          <p className={css.p}>{page.summary}</p>
        ) : null
      ) : (
        blocks.map((b, i) =>
          b.kind === 'heading' ? (
            <h3 key={i} className={css.h3}>
              {b.text}
            </h3>
          ) : (
            <p key={i} className={css.p}>
              {b.text}
            </p>
          ),
        )
      )}
      <div className={css.fb}>
        {voted ? (
          <span className={css.thx}>✓ 感谢你的反馈</span>
        ) : (
          <>
            <span>这篇文章有帮助吗？</span>
            <Btn tone="soft" size="sm" busy={feedback.isPending} onClick={helpful}>
              有帮助
            </Btn>
          </>
        )}
        <Btn tone="soft" size="sm" href={href('/tickets/new')}>
          仍未解决，提交工单
        </Btn>
      </div>
    </>
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
