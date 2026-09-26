/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatBytes / formatCount / formatMoney，依赖 ../../../core/router 的 navigate / useHashLocation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / Drawer / Empty / Input / QueryView / Segmented / Select / Switch / Table / Tag / useToast，依赖 ../../actions 的 endsIntent / useCan / useIntentKey，依赖 ./api 的 useTrafficPacks / useInvalidatePlans / packResponseSchema / TrafficPack / PackStatus，依赖 ./model 的 PackForm / packForm / packProblems / packBody / PACK_STATUS_VIEW，依赖 ./failure 的 useCatalogFailure，依赖 ./Plans.module.css
 * [OUTPUT]: 对外提供 PacksTab
 * [POS]: 套餐页「流量包」标签（#/plans/packs?s=<状态>，修订 R73；后端有、设计稿缺，按后台-04 的风格补）：状态分段、表格（名称与推荐、容量、价格、状态、已售、排序）、新建 / 编辑抽屉、上下架确认。乐观锁是列表里读到的 updated_at，409 时刷新到最新。写接口 catalog.publish + reauth + 幂等；新建、修改、上架受销售开关控制，下架不受
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatBytes, formatCount, formatMoney } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Drawer, Empty, Input, QueryView, Segmented, Select, Switch, Table, Tag, useToast, type TableColumn } from '../../../ui'
import { endsIntent, useCan, useIntentKey } from '../../actions'
import { packResponseSchema, useInvalidatePlans, useTrafficPacks, type PackStatus, type TrafficPack } from './api'
import { useCatalogFailure } from './failure'
import { PACK_STATUS_VIEW, packBody, packForm, packProblems, type PackForm } from './model'
import css from './Plans.module.css'

type Filter = PackStatus | 'all'
const FILTERS: ReadonlyArray<{ value: Filter; label: string }> = [
  { value: 'all', label: '全部' },
  { value: 'active', label: '在售' },
  { value: 'archived', label: '已下架' },
]
const CURRENCY_OPTIONS = [
  { value: 'CNY', label: 'CNY' },
  { value: 'USD', label: 'USD' },
]
const STALE = '流量包已被其他人修改，已刷新到最新；请确认后再操作'

export function PacksTab() {
  const location = useHashLocation()
  const s = location.query.get('s')
  const filter: Filter = s === 'active' || s === 'archived' ? s : 'all'
  const packs = useTrafficPacks(filter === 'all' ? '' : filter)
  const can = useCan()
  const writable = can('catalog.publish')
  const [editing, setEditing] = useState<TrafficPack | 'new' | null>(null)
  const [toggling, setToggling] = useState<TrafficPack | null>(null)

  const columns: TableColumn<TrafficPack>[] = [
    {
      key: 'name',
      header: '名称',
      render: (p) => (
        <span className={css.packName}>
          <span className={css.cardName}>{p.name}</span>
          {p.recommended && <Tag tone="brand">推荐</Tag>}
        </span>
      ),
    },
    { key: 'size', header: '容量', width: '110px', mono: true, render: (p) => formatBytes(p.traffic_bytes) },
    { key: 'price', header: '价格', width: '110px', mono: true, align: 'right', render: (p) => formatMoney(p.unit_amount, p.currency) },
    { key: 'status', header: '状态', width: '90px', render: (p) => <Tag tone={PACK_STATUS_VIEW[p.status].tone}>{PACK_STATUS_VIEW[p.status].label}</Tag> },
    { key: 'sold', header: '已售', width: '80px', mono: true, align: 'right', render: (p) => formatCount(p.sold_count) },
    { key: 'sort', header: '排序', width: '70px', mono: true, align: 'right', render: (p) => p.sort_order },
  ]
  if (writable)
    columns.push({
      key: 'actions',
      header: '',
      width: '140px',
      align: 'right',
      render: (p) => (
        <span className={css.rowActions}>
          <Button size="xs" variant="link" onClick={() => setEditing(p)}>
            编辑
          </Button>
          <Button size="xs" variant="ghost" onClick={() => setToggling(p)}>
            {p.status === 'active' ? '下架' : '上架'}
          </Button>
        </span>
      ),
    })

  return (
    <>
      <div className={css.toolbar}>
        <Segmented label="流量包状态" size="sm" options={FILTERS} value={filter} onChange={(v) => navigate('/plans/packs', { replace: true, query: { s: v === 'all' ? undefined : v } })} />
        <span className={css.small}>用户在门户「选购」里购买，买完立即叠加到流量余额，永不过期、用完为止</span>
        <span className={css.spacer} />
        {writable && (
          <Button size="sm" variant="primary" onClick={() => setEditing('new')}>
            ＋ 新建流量包
          </Button>
        )}
      </div>
      <QueryView
        query={packs}
        isEmpty={(d) => d.length === 0}
        empty={
          <Empty title={filter === 'archived' ? '没有已下架的流量包' : '还没有流量包'} description={writable ? '新建一个，用户在门户「选购」里就能买到。' : '有发布权限的管理员新建后会出现在这里。'} />
        }
      >
        {(rows) => (
          <div className={css.tableCard}>
            <Table label="流量包" columns={columns} rows={rows} rowKey={(p) => p.id} />
          </div>
        )}
      </QueryView>
      <PackDrawer pack={editing} onClose={() => setEditing(null)} />
      <StatusDialog pack={toggling} onClose={() => setToggling(null)} />
    </>
  )
}

function PackDrawer({ pack, onClose }: { pack: TrafficPack | 'new' | null; onClose: () => void }) {
  const current = pack === 'new' ? undefined : (pack ?? undefined)
  return <PackEditor key={pack === null ? 'closed' : pack === 'new' ? 'new' : `${pack.id}:${pack.updated_at}`} open={pack !== null} pack={current} onClose={onClose} />
}

function PackEditor({ open, pack, onClose }: { open: boolean; pack: TrafficPack | undefined; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const [form, setForm] = useState<PackForm>(() => packForm(pack))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof PackForm>(key: K, value: PackForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const save = async () => {
    const problems = packProblems(form)
    if (Object.keys(problems).length) return setErrors(problems)
    setBusy(true)
    try {
      if (pack) {
        const body = { ...packBody(form), expected_updated_at: pack.updated_at }
        await api.put(`v1/traffic-packs/${encodeURIComponent(pack.id)}`, packResponseSchema, { body, idempotencyKey: intent.keyFor([pack.id, body]) })
        toast('流量包已保存')
      } else {
        const body = packBody(form)
        await api.post('v1/traffic-packs', packResponseSchema, { body, idempotencyKey: intent.keyFor(body) })
        toast('流量包已上架')
      }
      intent.reset()
      onClose()
    } catch (e) {
      if (endsIntent(e)) intent.reset()
      if (isApiError(e, 'conflict') && e.fields.updated_at) {
        toast(STALE, 'danger')
        onClose()
      } else fail(e, setErrors)
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  return (
    <Drawer
      open={open}
      onClose={onClose}
      dismissible={!busy}
      title={pack ? '编辑流量包' : '新建流量包'}
      subtitle={pack ? `已售 ${formatCount(pack.sold_count)} 份；改价只影响之后的购买` : '新建即在售'}
      actions={
        <>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button variant="primary" busy={busy} onClick={() => void save()}>
            {pack ? '保存' : '新建并上架'}
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <Input label="名称" value={form.name} onChange={(e) => set('name', e.target.value)} error={errors.name} hint="1 到 60 个字，门户卡片上显示" data-autofocus />
        <div className={css.grid2}>
          <Input label="容量（GB）" mono inputMode="decimal" value={form.gb} onChange={(e) => set('gb', e.target.value)} error={errors.traffic_bytes} />
          <Input label="排序（越小越靠前）" mono inputMode="numeric" value={form.sortOrder} onChange={(e) => set('sortOrder', e.target.value)} error={errors.sort_order} />
          <Select label="币种" options={CURRENCY_OPTIONS} value={form.currency} onChange={(e) => set('currency', e.target.value as PackForm['currency'])} error={errors.currency} />
          <Input label="价格（元）" mono inputMode="decimal" value={form.amount} onChange={(e) => set('amount', e.target.value)} error={errors.unit_amount} />
        </div>
        <Switch label="标为推荐" checked={form.recommended} onChange={(e) => set('recommended', e.target.checked)} />
      </div>
    </Drawer>
  )
}

/** POST v1/traffic-packs/{id}/status：下架只影响之后的购买，已买的余量不受影响 */
function StatusDialog({ pack, onClose }: { pack: TrafficPack | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useCatalogFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidatePlans()
  const off = pack?.status === 'active'
  const confirm = async () => {
    if (!pack) return
    const body = { status: off ? 'archived' : 'active', expected_updated_at: pack.updated_at }
    try {
      await api.post(`v1/traffic-packs/${encodeURIComponent(pack.id)}/status`, packResponseSchema, { body, idempotencyKey: intent.keyFor([pack.id, body]) })
      intent.reset()
      toast(off ? `「${pack.name}」已下架` : `「${pack.name}」已重新上架`)
      onClose()
    } catch (e) {
      if (endsIntent(e)) intent.reset()
      if (isApiError(e, 'conflict')) {
        // updated_at 过期或已经是目标状态：刷新后按最新状态再决定
        toast(e.fields.updated_at ? STALE : e.message, 'danger')
        onClose()
      } else fail(e)
    } finally {
      void invalidate()
    }
  }
  return (
    <ConfirmModal
      open={pack !== null}
      title={off ? `下架「${pack?.name ?? ''}」？` : `重新上架「${pack?.name ?? ''}」？`}
      body={off ? '门户不再展示，之后买不到；已经买了的流量包余量不受影响。' : '门户重新可以购买。上架受销售开关控制。'}
      confirmLabel={off ? '下架' : '上架'}
      tone={off ? 'danger' : 'primary'}
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
