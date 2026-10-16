import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { Button, ConfirmModal, Input, Modal, QueryView, Select, Switch, Tabs, Tag, useToast, type TabItem } from '../../../ui'
import { usePlan, usePlanPools, type PlanDetail } from './api'
import css from './Detail.module.css'
import { BasicModal, OnceModal, ResetModal, SalesModal, UsageModal, usePlanWrite } from './EditModals'
import {
  activeTierPrice,
  ARCHIVE_BODY,
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
  type TierKey,
} from './model'

export function PlanDetailModal({
  id,
  tab,
  onTab,
  onClose,
}: {
  id: string | null
  tab: DetailTab
  onTab: (t: DetailTab) => void
  onClose: () => void
}) {
  const detail = usePlan(id)
  const plan = detail.data
  const draft = plan ? draftVersion(plan) : undefined
  const { busy, run } = usePlanWrite()
  const toast = useToast()

  const [editGroup, setEditGroup] = useState<OverviewGroupKey | null>(null)
  const [archiveOpen, setArchiveOpen] = useState(false)
  const [publishN, setPublishN] = useState<number | null>(null)

  const tabs = plan ? detailTabs(plan) : []
  const activeTab = tabs.some((t) => t.value === tab) ? tab : 'overview'
  const tabItems: TabItem<DetailTab>[] = tabs.map((t) => ({
    value: t.value,
    label: t.label,
    badge: t.count !== undefined ? t.count : undefined,
  }))

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

  return (
    <Modal
      open={id !== null}
      onClose={onClose}
      size={680}
      tall
      title={plan ? plan.name : '载入中…'}
      headExtra={
        plan ? (
          <>
            <Tag tone={PLAN_STATUS_VIEW[plan.status].tone}>{PLAN_STATUS_VIEW[plan.status].label}</Tag>
            {plan.recommended && <Tag tone="brand">推荐</Tag>}
            {plan.status === 'draft' && draft && (
              <Button variant="primary" size="sm" disabled={busy} onClick={() => setPublishN(draft.version)}>
                {`发布 v${draft.version}`}
              </Button>
            )}
          </>
        ) : undefined
      }
      subtitle={plan ? planSubtitle(plan) : undefined}
    >
      <QueryView query={detail} rows={6} empty="不存在或已删除">
        {(p) => (
          <div className={css.bodyStack}>
            <Tabs label="套餐详情" items={tabItems} value={activeTab} onChange={onTab} />
            {activeTab === 'overview' && (
              <OverviewTab plan={p} onEdit={(key) => setEditGroup(key)} onArchive={() => setArchiveOpen(true)} />
            )}
            {activeTab === 'prices' && <PricesTab plan={p} busy={busy} run={run} />}
            {activeTab === 'pools' && <PoolsTab plan={p} busy={busy} run={run} />}
            {activeTab === 'versions' && (
              <VersionsTab
                plan={p}
                busy={busy}
                onPublish={(n) => setPublishN(n)}
                onNewVersion={() => setEditGroup('usage')}
              />
            )}
          </div>
        )}
      </QueryView>

      {plan && editGroup === 'basic' && <BasicModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {plan && editGroup === 'usage' && <UsageModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {plan && editGroup === 'sales' && <SalesModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {plan && editGroup === 'reset' && <ResetModal open plan={plan} onClose={() => setEditGroup(null)} />}
      {plan && editGroup === 'once' && <OnceModal open plan={plan} onClose={() => setEditGroup(null)} />}

      {plan && archiveOpen && (
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

      {plan && publishN !== null && (
        <ConfirmModal
          open
          title={publishCopy(plan, publishN).title}
          body={publishCopy(plan, publishN).body}
          confirmLabel="发布"
          onCancel={() => setPublishN(null)}
          onConfirm={handlePublish}
        />
      )}
    </Modal>
  )
}

function OverviewTab({
  plan,
  onEdit,
  onArchive,
}: {
  plan: PlanDetail
  onEdit: (key: OverviewGroupKey) => void
  onArchive: () => void
}) {
  const notes = overviewNotes(plan)
  const groups = overviewGroups(plan)
  const live = isLive(plan)

  return (
    <div className={css.bodyStack}>
      {notes.length > 0 && (
        <div className={css.notes}>
          {notes.map((note) => (
            <div key={note} className={css.note}>
              <span className={css.noteDot} aria-hidden="true" />
              <span>{note}</span>
            </div>
          ))}
        </div>
      )}

      {groups.map((group) => (
        <section key={group.key} className={css.section}>
          <div className={css.sectionHeader}>
            <h3 className={css.sectionTitle}>{group.title}</h3>
            {live && (
              <Button size="sm" onClick={() => onEdit(group.key)}>
                编辑
              </Button>
            )}
          </div>
          {group.rows.map((row) => {
            const valClass = [css.kvVal, row.mono && css.mono, row.muted && css.muted].filter(Boolean).join(' ')
            return (
              <div key={row.k} className={css.kvRow}>
                <span className={css.kvKey}>{row.k}</span>
                <span className={valClass}>{row.v}</span>
              </div>
            )
          })}
        </section>
      ))}

      {live && (
        <div className={css.archiveRow}>
          <div className={css.archiveInfo}>
            <span className={css.archiveTitle}>归档这个套餐</span>
            <span className={css.mutedSmall}>用户端不再展示、不能新购，老用户不能续费；不可恢复</span>
          </div>
          <Button size="sm" variant="secondary" className={css.dangerBtn} onClick={onArchive}>
            归档
          </Button>
        </div>
      )}
    </div>
  )
}

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
    <section className={css.section}>
      <div className={css.sectionHeader}>
        <h3 className={css.sectionTitle}>在售价格</h3>
      </div>
      {rows.map((row) => (
        <div key={row.id} className={[css.priceRow, !row.active && css.dimmed].filter(Boolean).join(' ')}>
          <span className={css.kvKey}>{row.label}</span>
          <span className={css.mono}>{row.amount}</span>
          {row.active && live ? (
            <Button size="xs" variant="ghost" disabled={busy} onClick={() => void handleStop(row.tier)}>
              停售
            </Button>
          ) : row.active ? null : (
            <span className={css.mutedSmall}>已停售</span>
          )}
        </div>
      ))}
      {live && (
        <div className={css.priceAddRow}>
          <Select
            label="计费周期"
            options={tierOptions}
            value={newTier}
            onChange={(e) => {
              setNewTier(e.target.value as TierKey)
              setAddError(undefined)
            }}
          />
          <Input
            mono
            inputMode="decimal"
            placeholder="价格（元）"
            value={newAmount}
            onChange={(e) => {
              setNewAmount(e.target.value)
              setAddError(undefined)
            }}
            hint={hint}
            error={addError}
          />
          <Button variant="primary" size="sm" disabled={busy} onClick={() => void handleAdd()}>
            添加价格
          </Button>
        </div>
      )}
    </section>
  )
}

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
    <section className={css.section}>
      <div className={css.sectionHeader}>
        <h3 className={css.sectionTitle}>可用线路</h3>
        <span className={css.mutedSmall}>{`已选 ${plan.pool_ids.length} 个 · 改动立即生效`}</span>
      </div>

      <QueryView query={poolsQuery} rows={4} empty={null} isEmpty={() => false}>
        {(pools) => {
          const warning = live ? poolBindingWarning(pools, plan.pool_ids) : null
          return (
            <div className={css.stack}>
              {warning && (
                <div className={css.note}>
                  <span className={css.noteDot} aria-hidden="true" />
                  <span>{warning}</span>
                </div>
              )}
              {pools.length === 0 ? (
                <div className={css.mutedSmall}>还没有可用的节点池，先到「节点 › 节点池」建一个。</div>
              ) : (
                pools.map((pool) => {
                  const on = plan.pool_ids.includes(pool.id)
                  return (
                    <div key={pool.id} className={css.poolRow}>
                      <div className={css.poolInfo}>
                        <span className={css.poolName}>{pool.name}</span>
                        <span className={css.mutedSmall}>{poolMeta(pool)}</span>
                      </div>
                      <Switch
                        aria-label={pool.name}
                        checked={on}
                        disabled={busy || !live}
                        onChange={() => void handleToggle(pool.id, pool.name, on)}
                      />
                    </div>
                  )
                })
              )}
            </div>
          )
        }}
      </QueryView>
    </section>
  )
}

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
    <section className={css.section}>
      <div className={css.sectionHeader}>
        <h3 className={css.sectionTitle}>版本记录</h3>
        {live && (
          <Button
            size="sm"
            disabled={draft !== undefined}
            title={draft ? `已有草稿 v${draft.version}，先发布它` : undefined}
            onClick={onNewVersion}
          >
            新建版本
          </Button>
        )}
      </div>
      {rows.map((row) => (
        <div key={row.id} className={css.versionRow}>
          <span className={css.versionN}>{`v${row.n}`}</span>
          <div className={css.versionMeta}>
            <div className={css.versionHeader}>
              <Tag tone={row.tone}>{row.label}</Tag>
              <span className={css.versionDate}>{row.at}</span>
            </div>
            <div className={css.versionSummary}>
              {row.summary}
              {row.by ? ` · ${row.by}` : ''}
            </div>
          </div>
          {row.isDraft && (
            <Button variant="primary" size="xs" disabled={busy} onClick={() => onPublish(row.n)}>
              发布
            </Button>
          )}
        </div>
      ))}
    </section>
  )
}
