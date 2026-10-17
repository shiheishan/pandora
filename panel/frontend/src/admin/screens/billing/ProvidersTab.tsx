import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, QueryView, Switch, Table, Tag, Toolbar, useToast, type TableColumn } from '../../../ui'
import { PageActions } from '../../kit'
import { useFailure } from '../../actions'
import { toggledSchema, useInvalidateBilling, useProviders, type Provider } from './api'
import css from './Billing.module.css'
import { effectiveMethods, isEditableProvider, isOffline, methodLabel, providerKind, providerMode, providerNote, providerStats, providerTags, rateLabel, todayLabel, toggleBody, type ProviderMode } from './model'
import { CallbackBaseCard } from './CallbackBaseCard'
import { ProviderDialog } from './ProviderDialog'

interface Pending {
  p: Provider
  target: ProviderMode
}

export function ProvidersTab({ now }: { now: Date }) {
  const providers = useProviders()
  const [pending, setPending] = useState<Pending | null>(null)
  // null = 关；'new' = 新建；Provider = 编辑这一条
  const [form, setForm] = useState<Provider | 'new' | null>(null)

  return (
    <div className={css.page}>
      <PageActions>
        <Button variant="primary" icon={PlusSignIcon} onClick={() => setForm('new')}>
          新建渠道
        </Button>
      </PageActions>
      {/* 顶部统计由前端从列表算（不含系统内置的 offline）；数据没回来时不显示 */}
      <Toolbar>
        {providers.data && (
          <span className={css.tsum}>
            {providerStats(providers.data).map((s, i) => (
              <span key={s.label}>
                {i > 0 && ' · '}
                {s.tone === 'warn' ? (
                  <span className={css.tone_warn}>{`${s.n} ${s.label}`}</span>
                ) : (
                  `${s.n} ${s.label}`
                )}
              </span>
            ))}
          </span>
        )}
      </Toolbar>
      <QueryView query={providers} isEmpty={(d) => d.length === 0} empty={<Empty title="还没有支付渠道" description="点「新建渠道」接入易支付、BTCPay Server 或 CoinPayments。" />}>
        {(rows) => (
          <ProviderTable
            rows={rows}
            now={now}
            onToggle={(target, p) => setPending({ p, target })}
            onEdit={setForm}
          />
        )}
      </QueryView>
      {/* 「回调地址」卡是唯一配置回调前缀的地方，设计稿没画但保留在表格下方 */}
      <CallbackBaseCard />
      <ToggleDialog pending={pending} onClose={() => setPending(null)} />
      <ProviderDialog open={form !== null} editing={form === 'new' ? null : form} onClose={() => setForm(null)} />
    </div>
  )
}

function ProviderTable({
  rows,
  now,
  onToggle,
  onEdit,
}: {
  rows: Provider[]
  now: Date
  onToggle: (target: ProviderMode, p: Provider) => void
  onEdit: (p: Provider) => void
}) {
  const columns: TableColumn<Provider>[] = [
    {
      key: 'name',
      header: '渠道',
      render: (p) => {
        const offline = isOffline(p)
        const note = providerNote(p, now)
        const methods = p.adapter === 'epay' ? effectiveMethods(p) : []
        return (
          <span className={css.stack}>
            <span className={css.providerName}>
              <b>{p.display_name}</b>
              <span className={`${css.mono} ${css.muted}`}>{p.code}</span>
              <Tag tone="neutral">{providerKind(p)}</Tag>
              {providerTags(p).map((t) => (
                <Tag key={t.label} tone={t.tone}>{t.label}</Tag>
              ))}
            </span>
            <div className={css.providerLines}>
              <span className={note.tone === 'danger' ? css.tone_danger : undefined}>{note.text}</span>
              {methods.length > 0 && <span>支付方式：{methods.map(methodLabel).join(' / ')}</span>}
              {!offline && p.notify_url && (
                <span>
                  回调地址：<span className={css.mono}>{p.notify_url}</span>
                </span>
              )}
              <span className="m-only">
                今日成交 {todayLabel(p.today)} · 成功率 {rateLabel(p.success_rate_24h)}
              </span>
            </div>
          </span>
        )
      },
    },
    { key: 'today', header: '今日成交', width: '110px', align: 'right', mono: true, hideOnMobile: true, render: (p) => todayLabel(p.today) },
    {
      key: 'rate',
      header: (
        <span title="近 24 小时进入终态的支付尝试里成功的比例">成功率</span>
      ),
      width: '90px',
      align: 'right',
      mono: true,
      hideOnMobile: true,
      render: (p) => rateLabel(p.success_rate_24h),
    },
    {
      key: 'act',
      header: '收新单 · 操作',
      width: '200px',
      align: 'right',
      render: (p) => {
        const offline = isOffline(p)
        const mode = providerMode(p)
        if (offline) {
          return <span className={css.muted}>内置，不能改</span>
        }
        return (
          // 行内控件不触发整行的「打开编辑」
          <span className={css.rowActions} onClick={(e) => e.stopPropagation()}>
            <Switch aria-label={`${p.display_name} 收新单`} checked={mode === 'on'} onChange={(e) => onToggle(e.target.checked ? 'on' : 'paused', p)} />
            {isEditableProvider(p) && (
              <Button variant="link" size="xs" onClick={() => onEdit(p)}>
                编辑
              </Button>
            )}
            {mode !== 'off' && (
              <button type="button" className={css.textAction} onClick={() => onToggle('off', p)}>
                停用
              </button>
            )}
          </span>
        )
      },
    },
  ]
  // 整行点击打开编辑；系统内置的 offline 渠道只读，不响应
  return <Table label="支付渠道列表" columns={columns} rows={rows} rowKey={(p) => p.id} boxed onRowClick={(p) => isEditableProvider(p) && onEdit(p)} />
}

const COPY: Record<ProviderMode, { title: (n: string) => string; body: string; confirm: string; danger: boolean }> = {
  on: { title: (n) => `启用 ${n}？`, body: '启用后用户端结账页立即可见。', confirm: '启用', danger: false },
  paused: { title: (n) => `停用 ${n}？`, body: '用户端结账页将不再显示该渠道，进行中的支付仍会回调。', confirm: '停用', danger: true },
  off: {
    title: (n) => `完全停用 ${n}？`,
    body: '结账页不再显示，渠道回调也不再处理：进行中的支付即使到账也不会自动入账，需要人工核对后「标记已支付」。一般用「停用」就够了。',
    confirm: '完全停用',
    danger: true,
  },
}

/** POST v1/payment-providers/{code}/toggle：两个布尔都要传（漏传按 false） */
function ToggleDialog({ pending, onClose }: { pending: Pending | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateBilling()
  const copy = pending ? COPY[pending.target] : COPY.on
  const confirm = async () => {
    if (!pending) return
    try {
      await api.post(`v1/payment-providers/${encodeURIComponent(pending.p.code)}/toggle`, toggledSchema, { body: toggleBody(pending.target) })
      toast(pending.target === 'on' ? `${pending.p.display_name} 已启用` : pending.target === 'paused' ? `${pending.p.display_name} 已停止收新单` : `${pending.p.display_name} 已完全停用`)
      onClose()
    } catch (e) {
      fail(e)
    } finally {
      void invalidate()
    }
  }
  const noCreds = pending?.target === 'on' && !pending.p.has_credentials
  return (
    <ConfirmModal
      open={pending !== null}
      title={copy.title(pending?.p.display_name ?? '')}
      body={noCreds ? `${copy.body}注意：这个渠道还没有配置凭据，用户选了也付不了款。` : copy.body}
      confirmLabel={copy.confirm}
      tone={copy.danger ? 'danger' : 'primary'}
      onCancel={onClose}
      onConfirm={confirm}
    />
  )
}
