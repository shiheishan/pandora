import { useState } from 'react'
import { Add01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { navigate, useHashLocation } from '../../../core/router'
import { Button, Empty, FilterTabs, IconClose, Modal, QueryView, Table, Tag, Toolbar } from '../../../ui'
import { PageActions } from '../../kit'
import { AnnounceEditor } from './AnnounceEditor'
import css from './announce.module.css'
import { ANN_STATUS, annFilterItems, annTargetLabel, annWhen, isAnnFilter, SEVERITY, visibleAnns, type AnnFilter, type AnnForm } from './logic'
import { useAnnouncements } from './queries'
import type { Announcement, Severity } from './schemas'

/** 列表路径：筛选放在 ?f= 上，all 不写进地址 */
const LIST_PATH = '/content/announce'

/** 级别文字的颜色类（普通灰、提示 info、警告 warn、紧急 danger） */
const SEV_CLASS: Record<Severity, string | undefined> = { info: css.sev_info, notice: css.sev_notice, warning: css.sev_warning, critical: css.sev_critical }

export function AnnounceTab({ rest }: { rest: string[] }) {
  const query = useAnnouncements()
  const { query: urlQuery } = useHashLocation()
  // 地址上的筛选；没有或不认识就是全部
  const raw = urlQuery.get('f')
  const filter: AnnFilter = isAnnFilter(raw) ? raw : 'all'
  // 「复制为新公告」的预填；每次复制换一个 seed，让新建弹窗重新挂载
  const [draft, setDraft] = useState<{ seed: number; form: AnnForm | null }>({ seed: 0, form: null })
  const selected = rest[0] ?? null

  /** 当前筛选的地址值：all 不写进地址 */
  const fQuery = filter === 'all' ? undefined : { f: filter }
  const setFilter = (next: AnnFilter) => {
    // 开着弹窗切筛选也只回到列表路径
    navigate(LIST_PATH, { replace: true, query: next === 'all' ? undefined : { f: next } })
  }
  const open = (id: string) => navigate(`${LIST_PATH}/${id}`, { query: fQuery })
  const close = () => navigate(LIST_PATH, { query: fQuery })
  const startNew = (form: AnnForm | null) => {
    setDraft((d) => ({ seed: d.seed + 1, form }))
    open('new')
  }

  return (
    <>
      <PageActions>
        <Button variant="primary" icon={Add01Icon} onClick={() => startNew(null)}>
          新建公告
        </Button>
      </PageActions>
      <QueryView query={query} rows={5} empty={null}>
        {(data) => {
          const list = data.announcements
          const current: Announcement | 'new' | null = selected === 'new' ? 'new' : (list.find((a) => a.id === selected) ?? null)
          return (
            <div className={css.page}>
              <Toolbar>
                <FilterTabs label="按状态筛选" items={annFilterItems(list)} value={filter} onChange={setFilter} />
              </Toolbar>
              <Table
                boxed
                label="公告列表"
                rows={visibleAnns(list, filter)}
                rowKey={(a) => a.id}
                rowClassName={(a) => (a.status === 'withdrawn' ? css.dimRow : undefined)}
                onRowClick={(a) => open(a.id)}
                columns={[
                  {
                    key: 'title',
                    header: '标题',
                    render: (a) => (
                      <span className={css.annTitle}>
                        {a.pinned && <Tag tone="neutral">置顶</Tag>}
                        <span className={css.annTitleText}>{a.title}</span>
                      </span>
                    ),
                  },
                  {
                    key: 'status',
                    header: '状态',
                    width: '72px',
                    render: (a) => (
                      <Tag tone={ANN_STATUS[a.status].tone}>{ANN_STATUS[a.status].label}</Tag>
                    ),
                  },
                  {
                    key: 'severity',
                    header: '级别',
                    hideOnMobile: true,
                    width: '64px',
                    render: (a) => <span className={SEV_CLASS[a.severity]}>{SEVERITY[a.severity].label}</span>,
                  },
                  {
                    key: 'target',
                    header: '可见范围',
                    hideOnMobile: true,
                    width: '104px',
                    render: (a) => <span className={css.target}>{annTargetLabel(a)}</span>,
                  },
                  {
                    key: 'when',
                    header: '时间',
                    hideOnMobile: true,
                    width: '112px',
                    align: 'right',
                    render: (a) => <span className={css.when}>{annWhen(a)}</span>,
                  },
                ]}
                empty={
                  list.length === 0 ? (
                    <Empty bare title="还没有公告" description="新建一条公告，发布后用户端「消息」页与首页公告栏都能看到。" />
                  ) : (
                    <Empty bare title="这个状态下没有公告" />
                  )
                }
              />
              {selected === null ? null : current === 'new' ? (
                <AnnounceEditor key={`new:${draft.seed}`} data={data} original={null} prefill={draft.form} onCopy={startNew} onClose={close} />
              ) : current ? (
                <AnnounceEditor key={current.id} data={data} original={current} prefill={null} onCopy={startNew} onClose={close} />
              ) : (
                <Modal
                  open
                  size={640}
                  title="公告"
                  onClose={close}
                  headExtra={
                    <Button variant="ghost" size="sm" aria-label="关闭" onClick={close}>
                      <IconClose />
                    </Button>
                  }
                >
                  <Empty bare title="公告不存在或已删除" />
                </Modal>
              )}
            </div>
          )
        }}
      </QueryView>
    </>
  )
}
