import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Button, ConfirmModal, Input, QueryView, Select, Switch, Tag, useToast } from '../../../ui'
import { Callout, DangerRow, DetailPage, KvList, KvRow, Row, RowList, SectionTitle, SideFact, SideFacts } from '../../kit'
import { usePlan, usePlanPools, type PlanDetail } from './api'
import css from './Detail.module.css'
import { BasicModal, OnceModal, ResetModal, SalesModal, UsageModal, usePlanWrite } from './EditModals'
import {
  activeTierPrice,
  ARCHIVE_BODY,
  basicFacts,
  detailTabs,
  draftVersion,
  isLive,
  overviewGroups,
  overviewNotes,
  parseAmount,
  PLAN_STATUS_VIEW,
  planSubtitle,
  poolBindingWarning,
  poolMeta,
  poolsBody,
  priceListRows,
  publishCopy,
  rowVersionBody,
  tierPriceBody,
  TIERS,
  togglePool,
  versionListRows,
  type DetailTab,
  type OverviewGroupKey,
  type PriceListRow,
  type TierKey,
} from './model'

/** 按 mono / muted 标志拼值文本的类；两个都没有时回 undefined，避免空 className */
function valClass(row: { mono?: boolean; muted?: boolean }): string | undefined {
  return [row.mono ? css.mono : undefined, row.muted ? css.muted : undefined].filter(Boolean).join(' ') || undefined
}

/** 套餐详情整页（样稿 .dpage）：地址 /plans/<id>[/<tab>]，返回链接与页签由外层的 index.tsx 给 */
export function PlanDetailPage({
  id,
  tab,
  back,
  onTab,
}: {
  id: string
  tab: DetailTab
  back: { href: string; label: string }
  onTab: (t: DetailTab) => void
}) {
  const detail = usePlan(id)
  const plan = detail.data
  const draft = plan ? draftVersion(plan) : undefined
  const { busy, run } = usePlanWrite()
  const toast = useToast()

  const [editGroup, setEditGroup] = useState<OverviewGroupKey | null>(null)
  const [archiveOpen, setArchiveOpen] = useState(false)
  const [publishN, setPublishN] = useState<number | null>(null)

  const handleArchive = async () => {
    if (!plan) return
    const res = await run('post', `v1/plans/${encodeURIComponent(plan.id)}/archive`, rowVersionBody(plan.row_version))
    if (res) {
      toast('已归档')
      setArchiveOpen(false)
    }
  }

  const handlePublish = async () => {
    if (!plan || publishN === null) return
    const d = draftVersion(plan)
    if (!d) return
    const res = await run(
      'post',
      `v1/plans/${encodeURIComponent(plan.id)}/versions/${d.id}/publish`,
      rowVersionBody(plan.row_version),
    )
    if (res) {
      toast(`v${publishN} 已发布`)
      setPublishN(null)
    }
  }

  // 数据没回来（加载中 / 出错 / 404）：只画骨架，「不存在或已删除」与重试交给 QueryView
  if (!plan) {
    return (
      <DetailPage back={back} title="套餐">
        <QueryView query={detail} rows={6} empty={null}>
          {() => null}
        </QueryView>
      </DetailPage>
    )
  }

  const live = isLive(plan)
  const tabs = detailTabs(plan)
  const activeTab = tabs.some((t) => t.value === tab) ? tab : 'overview'
  const statusView = PLAN_STATUS_VIEW[plan.status]
  const publishConfirm = publishN !== null ? publishCopy(plan, publishN) : null

  return (
    <>
      <DetailPage
        back={back}
        title={plan.name}
        meta={
          <>
            <Tag tone={statusView.tone}>{statusView.label}</Tag>
            {plan.recommended && <Tag tone="info">推荐</Tag>}
            <span>{planSubtitle(plan)}</span>
          </>
        }
        actions={
          live && draft ? (
            <Button variant="primary" size="sm" disabled={busy} onClick={() => setPublishN(draft.version)}>
              {`发布 v${draft.version}`}
            </Button>
          ) : undefined
        }
        tabs={{
          label: '套餐详情',
          items: tabs.map((t) => ({ value: t.value, label: t.label, badge: t.count })),
          value: activeTab,
          onChange: onTab,
        }}
        side={
          <div className={css.stack}>
            <SectionTitle
              action={
                live ? (
                  <Button variant="link" size="xs" onClick={() => setEditGroup('basic')}>
                    编辑
                  </Button>
                ) : undefined
              }
            >
              基本资料
            </SectionTitle>
            <SideFacts label="基本资料">
              {basicFacts(plan).map((f) => (
                <SideFact key={f.k} label={f.k}>
                  <span className={valClass(f)}>{f.v}</span>
                  {f.hint && <span className={css.muted}> · {f.hint}</span>}
                </SideFact>
              ))}
            </SideFacts>
          </div>
        }
      >
        {activeTab === 'overview' && (
          <OverviewTab
            plan={plan}
            live={live}
            busy={busy}
            onEdit={setEditGroup}
            onArchive={() => setArchiveOpen(true)}
            onTab={onTab}
          />
        )}
        {activeTab === 'prices' && <PricesTab plan={plan} busy={busy} run={run} />}
        {activeTab === 'pools' && <PoolsTab plan={plan} busy={busy} run={run} />}
        {activeTab === 'versions' && (
          <VersionsTab
            plan={plan}
            busy={busy}
            onPublish={(n) => setPublishN(n)}
            onNewVersion={() => setEditGroup('usage')}
          />
        )}
      </DetailPage>

      {editGroup === 'basic' && <BasicModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {editGroup === 'usage' && <UsageModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {editGroup === 'sales' && <SalesModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {editGroup === 'reset' && <ResetModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {editGroup === 'once' && <OnceModal open plan={plan} onClose={() => setEditGroup(null)} />}

      {archiveOpen && (
        <ConfirmModal
          open
          tone="danger"
          title={`归档「${plan.name}」？`}
          body={ARCHIVE_BODY}
          confirmLabel="归档"
          onCancel={() => setArchiveOpen(false)}
          onConfirm={handleArchive}
        />
      )}

      {publishConfirm && (
        <ConfirmModal
          open
          title={publishConfirm.title}
          body={publishConfirm.body}
          confirmLabel="发布"
          onCancel={() => setPublishN(null)}
          onConfirm={handlePublish}
        />
      )}
    </>
  )
}

/** 概览：提示条 + 各组只读键值 + 归档危险行 */
function OverviewTab({
  plan,
  live,
  busy,
  onEdit,
  onArchive,
  onTab,
}: {
  plan: PlanDetail
  live: boolean
  busy: boolean
  onEdit: (key: OverviewGroupKey) => void
  onArchive: () => void
  onTab: (t: DetailTab) => void
}) {
  return (
    <>
      {overviewNotes(plan).map((n) => {
        const action = n.action
        return (
          <Callout
            key={n.text}
            action={action ? <Button size="sm" onClick={() => onTab(action.tab)}>{action.label}</Button> : undefined}
          >
            {n.text}
          </Callout>
        )
      })}

      {overviewGroups(plan).map((group) => (
        <section key={group.key}>
          <SectionTitle
            action={
              live ? (
                <Button variant="link" size="xs" onClick={() => onEdit(group.key)}>
                  编辑
                </Button>
              ) : undefined
            }
          >
            {group.title}
          </SectionTitle>
          <KvList>
            {group.rows.map((row) => (
              <KvRow key={row.k} label={row.k}>
                <span className={valClass(row)}>{row.v}</span>
              </KvRow>
            ))}
          </KvList>
        </section>
      ))}

      {live && (
        <DangerRow
          title="归档这个套餐"
          description="用户端不再展示、不能新购，老用户不能续费；不可恢复"
          actionLabel="归档"
          disabled={busy}
          onAction={onArchive}
        />
      )}
    </>
  )
}

/** 价格（只有订阅套餐有这个页签）：在售与已停售的各档，live 时底部添加价格 */
function PricesTab({
  plan,
  busy,
  run,
}: {
  plan: PlanDetail
  busy: boolean
  run: ReturnType<typeof usePlanWrite>['run']
}) {
  const toast = useToast()
  const rows = priceListRows(plan)
  const live = isLive(plan)
  const [newTier, setNewTier] = useState<TierKey>('month:1')
  const [newAmount, setNewAmount] = useState('')
  const [addError, setAddError] = useState<string | undefined>()

  const existing = activeTierPrice(plan, newTier)
  const hint = existing ? `这一档在售 ${formatMoney(existing.unit_amount)}，添加后替换` : undefined

  const handleStop = async (tier: TierKey | null) => {
    if (!tier) return
    const res = await run(
      'put',
      `v1/plans/${encodeURIComponent(plan.id)}/prices`,
      tierPriceBody(plan.row_version, tier, null),
    )
    if (res) {
      toast('这一档已停售')
    }
  }

  const handleRestore = async (row: PriceListRow) => {
    if (!row.tier) return
    const res = await run(
      'put',
      `v1/plans/${encodeURIComponent(plan.id)}/prices`,
      tierPriceBody(plan.row_version, row.tier, row.unitAmount),
    )
    if (res) {
      toast('已恢复售卖')
    }
  }

  const handleAdd = async () => {
    const cents = parseAmount(newAmount)
    if (cents === null) {
      setAddError('填价格，最多两位小数')
      return
    }
    const res = await run(
      'put',
      `v1/plans/${encodeURIComponent(plan.id)}/prices`,
      tierPriceBody(plan.row_version, newTier, cents),
      {
        onFields: (fields) => {
          setAddError(Object.values(fields).join('；'))
        },
      },
    )
    if (res) {
      setNewAmount('')
      setAddError(undefined)
      toast('价格已保存')
    }
  }

  const tierOptions = TIERS.map((t) => ({ value: t.key, label: t.label }))

  return (
    <>
      <section>
        <SectionTitle>价格</SectionTitle>
        <KvList>
          {rows.map((row) => (
            <KvRow
              key={row.id}
              label={row.label}
              muted={!row.active}
              action={
                row.active && live ? (
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={busy}
                    aria-label={`停售${row.label}价格`}
                    onClick={() => void handleStop(row.tier)}
                  >
                    停售
                  </Button>
                ) : row.restorable && live ? (
                  <Button
                    size="xs"
                    variant="secondary"
                    disabled={busy}
                    aria-label={`恢复售卖${row.label}价格`}
                    onClick={() => void handleRestore(row)}
                  >
                    恢复售卖
                  </Button>
                ) : undefined
              }
            >
              <Tag tone={row.status.tone} className={css.priceTag}>
                {row.status.label}
              </Tag>
              <span className={css.mono}>{row.amount}</span>
              {row.note !== '' && <span className={css.muted}> · {row.note}</span>}
            </KvRow>
          ))}
        </KvList>
      </section>
      {live && (
        <section>
          <div className={css.addRow}>
            <Select
              aria-label="计费周期"
              fieldClassName={css.addTier}
              options={tierOptions}
              value={newTier}
              onChange={(e) => {
                setNewTier(e.target.value as TierKey)
                setAddError(undefined)
              }}
            />
            <Input
              mono
              fieldClassName={css.addAmount}
              inputMode="decimal"
              aria-label="价格（元）"
              placeholder="价格（元）"
              value={newAmount}
              onChange={(e) => {
                setNewAmount(e.target.value)
                setAddError(undefined)
              }}
              hint={hint}
              error={addError}
            />
            <Button size="sm" disabled={busy} onClick={() => void handleAdd()}>
              添加价格
            </Button>
          </div>
        </section>
      )}
    </>
  )
}

/** 线路：按详情里的 pool_ids 画开关，改动立即生效 */
function PoolsTab({
  plan,
  busy,
  run,
}: {
  plan: PlanDetail
  busy: boolean
  run: ReturnType<typeof usePlanWrite>['run']
}) {
  const toast = useToast()
  const poolsQuery = usePlanPools(plan.id)
  const live = isLive(plan)

  const handleToggle = async (poolId: string, poolName: string, currentlyBound: boolean) => {
    const nextIds = togglePool(plan.pool_ids, poolId)
    const res = await run('put', `v1/plans/${encodeURIComponent(plan.id)}/pools`, poolsBody(plan.row_version, nextIds))
    if (res) {
      toast(currentlyBound ? `已移除「${poolName}」` : `已加入「${poolName}」`)
    }
  }

  return (
    <section>
      <SectionTitle hint={`已选 ${plan.pool_ids.length} 个 · 改动立即生效`}>可用线路</SectionTitle>
      <QueryView query={poolsQuery} rows={4} empty={null} isEmpty={() => false}>
        {(pools) => {
          const warning = live ? poolBindingWarning(pools, plan.pool_ids) : null
          return (
            <>
              {warning && <Callout>{warning}</Callout>}
              {pools.length === 0 ? (
                <p className={css.mutedSmall}>还没有可用的节点池，先到「节点 › 节点池」建一个。</p>
              ) : (
                <RowList label="可用线路">
                  {pools.map((pool) => {
                    const on = plan.pool_ids.includes(pool.id)
                    return (
                      <Row
                        key={pool.id}
                        title={pool.name}
                        meta={poolMeta(pool)}
                        actions={
                          <Switch
                            aria-label={pool.name}
                            checked={on}
                            disabled={busy || !live}
                            onChange={() => void handleToggle(pool.id, pool.name, on)}
                          />
                        }
                      />
                    )
                  })}
                </RowList>
              )}
            </>
          )
        }}
      </QueryView>
    </section>
  )
}

/** 版本记录：草稿可发布，live 时可新建版本（打开用量弹窗） */
function VersionsTab({
  plan,
  busy,
  onPublish,
  onNewVersion,
}: {
  plan: PlanDetail
  busy: boolean
  onPublish: (n: number) => void
  onNewVersion: () => void
}) {
  const rows = versionListRows(plan)
  const draft = draftVersion(plan)
  const live = isLive(plan)

  return (
    <section>
      <SectionTitle
        action={
          live ? (
            <Button
              size="sm"
              disabled={draft !== undefined}
              title={draft ? `已有草稿 v${draft.version}，先发布它` : undefined}
              onClick={onNewVersion}
            >
              新建版本
            </Button>
          ) : undefined
        }
      >
        版本记录
      </SectionTitle>
      <RowList label="版本记录">
        {rows.map((row) => (
          <Row
            key={row.id}
            lead={
              <span className={css.versionLead}>
                <b className={css.versionN}>{`v${row.n}`}</b>
                <Tag tone={row.tone}>{row.label}</Tag>
              </span>
            }
            title={row.summary}
            meta={`${row.at}${row.by ? ` · ${row.by}` : ''}`}
            actions={
              row.isDraft && live ? (
                <Button variant="primary" size="sm" disabled={busy} onClick={() => onPublish(row.n)}>
                  发布
                </Button>
              ) : undefined
            }
          />
        ))}
      </RowList>
    </section>
  )
}
