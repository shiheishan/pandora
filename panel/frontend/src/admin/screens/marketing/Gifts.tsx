/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/format 的 formatMoney，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./GenerateCodes、./TemplateDrawer、./logic、./parts、./queries、./schemas，依赖 ./marketing.module.css 与 ./Gifts.module.css
 * [OUTPUT]: 对外提供 Gifts（营销 · 礼品卡标签）
 * [POS]: admin/screens/marketing 的礼品卡标签（设计稿 t_gifts）：四个统计、分段「模板 / 批次与卡码 / 使用记录」。子视图与选中批次记在 rest 里（#/marketing/gifts/batches/<批次 id>），刷新与分享都落在同一处。卡码全是掩码（R17），完整明文只有一次性导出
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { formatMoney } from '../../../core/format'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Segmented, Select, Tag, useToast } from '../../../ui'
import { GenerateModal, OneTimeModal, useBatchExport } from './GenerateCodes'
import { TemplateDrawer } from './TemplateDrawer'
import local from './Gifts.module.css'
import { CODE_STATUS, batchLabel, formatDate, formatDateTime, grantedLabel, redeemRate, templateFace } from './logic'
import css from './marketing.module.css'
import { Pager, QueryView, StatStrip } from './parts'
import { useCan, useFailure, useGiftBatches, useGiftCodes, useGiftStats, useGiftTemplates, useGiftUsages, useInvalidateMarketing, usePlanCatalog } from './queries'
import { toggleResponse, type Batch, type CodesGenerated, type GiftCode, type GiftStats, type GiftTemplate } from './schemas'

type View = 'templates' | 'batches' | 'usages'
const VIEWS: readonly View[] = ['templates', 'batches', 'usages']
const giftPath = (view: View, batchId?: string) => `/marketing/gifts/${view}${batchId ? `/${encodeURIComponent(batchId)}` : ''}`

/** 面额合计是待补·后端的 balance_issued；缺时退回已兑出余额并改标签（契约） */
function giftStats(s: GiftStats) {
  return [
    { label: '已发行', value: s.codes_total.toLocaleString('zh-CN') },
    { label: '已兑换', value: s.codes_used.toLocaleString('zh-CN') },
    { label: '兑换率', value: redeemRate(s.codes_used, s.codes_total) },
    s.balance_issued === undefined ? { label: '已兑出余额', value: formatMoney(s.balance_out, 'CNY') } : { label: '面额合计', value: formatMoney(s.balance_issued, 'CNY') },
  ]
}

export function Gifts({ rest }: { rest: string[] }) {
  const view: View = VIEWS.includes(rest[0] as View) ? (rest[0] as View) : 'templates'
  const stats = useGiftStats()
  const [generated, setGenerated] = useState<CodesGenerated | null>(null)

  return (
    <div className={css.stack}>
      <StatStrip label="礼品卡统计" items={stats.data ? giftStats(stats.data) : undefined} />
      <div className={css.toolbar}>
        <Segmented<View>
          label="礼品卡视图"
          size="sm"
          value={view}
          onChange={(v) => navigate(giftPath(v))}
          options={[
            { value: 'templates', label: '模板' },
            { value: 'batches', label: '批次与卡码' },
            { value: 'usages', label: '使用记录' },
          ]}
        />
      </div>
      {view === 'templates' && <Templates onGenerated={setGenerated} />}
      {view === 'batches' && <Batches selected={rest[1] ?? null} />}
      {view === 'usages' && <Usages />}
      <OneTimeModal
        result={generated}
        onClose={() => {
          if (generated) navigate(giftPath('batches', generated.batch_id))
          setGenerated(null)
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 模板
// ---------------------------------------------------------------------------
function Templates({ onGenerated }: { onGenerated: (result: CodesGenerated) => void }) {
  const can = useCan()
  const list = useGiftTemplates()
  const catalog = usePlanCatalog()
  const plans = catalog.isSuccess ? catalog.data : undefined
  const writable = can('marketing.giftcard.write')
  const [editing, setEditing] = useState<GiftTemplate | null | 'new'>(null)
  const [generating, setGenerating] = useState<GiftTemplate | null>(null)

  return (
    <>
      <QueryView
        query={list}
        rows={2}
        isEmpty={(d) => d.length === 0 && !writable}
        empty={<Empty title="还没有礼品卡模板" description="有礼品卡写权限的同事可以在这里新建模板并生成卡码。" />}
      >
        {(templates) => (
          <div className={local.cards}>
            {templates.map((t) => {
              const face = templateFace(t, plans)
              return (
                <section key={t.id} className={local.card}>
                  <div className={local.faceLine}>
                    <span className={local.face}>{face.face}</span>
                    <span className={css.muted}>{face.kind}</span>
                    <span className={css.spacer} />
                    {t.status === 'paused' && <Tag tone="warn">暂停兑换</Tag>}
                  </div>
                  <div className={local.cardName}>
                    <span className={local.swatch} style={{ background: t.theme_color || 'var(--text-3)' }} aria-hidden="true" />
                    {t.name}
                  </div>
                  <div className={css.faint}>
                    兑换后 {face.after} · 已发 {t.code_total.toLocaleString('zh-CN')} 张 · 已兑 {t.code_used.toLocaleString('zh-CN')}
                  </div>
                  {writable && (
                    <div className={local.cardActions}>
                      <Button size="sm" onClick={() => setGenerating(t)}>
                        生成一批码
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => setEditing(t)}>
                        编辑
                      </Button>
                    </div>
                  )}
                </section>
              )
            })}
            {writable && (
              <button type="button" className={local.newCard} onClick={() => setEditing('new')}>
                ＋ 新建模板
              </button>
            )}
          </div>
        )}
      </QueryView>
      <TemplateDrawer open={editing !== null} template={editing === 'new' ? null : editing} plans={plans} onClose={() => setEditing(null)} />
      <GenerateModal
        template={generating}
        onClose={() => setGenerating(null)}
        onGenerated={(result) => {
          setGenerating(null)
          onGenerated(result)
        }}
      />
    </>
  )
}

// ---------------------------------------------------------------------------
// 批次与卡码
// ---------------------------------------------------------------------------
const BATCH_PAGE = 50
const CODE_PAGE = 50

function Batches({ selected }: { selected: string | null }) {
  const [offset, setOffset] = useState(0)
  const list = useGiftBatches({ limit: BATCH_PAGE, offset })
  // 出错时 react-query 仍留着上一次的数据：右栏不能继续展示一个已经读不到的批次
  const items = list.isError ? [] : (list.data?.items ?? [])
  const current = items.find((b) => b.id === selected) ?? (selected ? undefined : items[0])

  return (
    <div className={local.split}>
      <div className={css.panel}>
        <QueryView query={list} isEmpty={(d) => d.items.length === 0} empty={<Empty bare title="还没有批次" description="在「模板」里给一张礼品卡生成一批码，这里就会出现。" />}>
          {(data) => (
            <>
              {data.items.map((b) => (
                <button key={b.id} type="button" className={b.id === current?.id ? `${local.batch} ${local.batchOn}` : local.batch} aria-current={b.id === current?.id} onClick={() => navigate(giftPath('batches', b.id))}>
                  <span className={local.batchTop}>
                    <span className={css.mono}>{batchLabel(b)}</span>
                    <span className={css.spacer} />
                    <span className={`${css.mono} ${css.faint}`}>
                      {b.used}/{b.count}
                    </span>
                  </span>
                  <span className={css.faint}>
                    {b.template_name} · {formatDate(b.created_at)}
                  </span>
                  <span className={b.exported_at ? css.faint : local.pending}>{b.exported_at ? `已导出${b.exported_by_email ? ` · ${b.exported_by_email}` : ''}` : '未导出 · 完整卡码仍可一次性导出'}</span>
                </button>
              ))}
              <Pager total={data.total} limit={BATCH_PAGE} offset={offset} onChange={setOffset} />
            </>
          )}
        </QueryView>
      </div>
      {current ? <BatchCodes key={current.id} batch={current} /> : list.isSuccess && selected && <Empty title="批次不在这一页" description="它可能在别的分页里，或者已经不存在。" />}
    </div>
  )
}

function BatchCodes({ batch }: { batch: Batch }) {
  const can = useCan()
  const writable = can('marketing.giftcard.write')
  const [offset, setOffset] = useState(0)
  const [confirm, setConfirm] = useState(false)
  const codes = useGiftCodes({ batch_id: batch.id, limit: CODE_PAGE, offset })
  const exporter = useBatchExport(() => setConfirm(false))

  return (
    <div className={css.panel}>
      <div className={css.panelHead}>
        <h3 className={css.panelTitle}>批次 {batchLabel(batch)} 的码</h3>
        {batch.expires_at && <span className={css.faint}>有效期至 {formatDate(batch.expires_at)}</span>}
        <span className={css.spacer} />
        {writable && (
          <Button size="xs" disabled={batch.exported_at !== null} onClick={() => setConfirm(true)}>
            {batch.exported_at ? '已导出过' : '一次性导出'}
          </Button>
        )}
      </div>
      <QueryView query={codes} isEmpty={(d) => d.codes.length === 0} empty={<Empty bare title="这个批次没有码" />}>
        {(data) => (
          <>
            {data.codes.map((c) => (
              <CodeRow key={c.id} code={c} writable={writable} />
            ))}
            <Pager total={data.total} limit={CODE_PAGE} offset={offset} onChange={setOffset} />
          </>
        )}
      </QueryView>
      <ConfirmModal
        open={confirm}
        title="一次性导出这批卡码？"
        body={`导出 ${batch.count} 张完整卡码的 CSV。导出后完整卡码不再可见，文件丢了就再也拿不回明文，请存到安全的地方。`}
        confirmLabel="导出 CSV"
        onConfirm={() => exporter.mutateAsync(batch.id).catch(() => setConfirm(false))}
        onCancel={() => setConfirm(false)}
      />
    </div>
  )
}

function CodeRow({ code: c, writable }: { code: GiftCode; writable: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const status = CODE_STATUS[c.status] ?? { label: c.status, tone: 'neutral' as const }
  // 停用不要 reauth：发现异常要能立刻止血（契约有意设计）
  const toggle = useMutation({
    mutationFn: (disabled: boolean) => api.post(`v1/gift-cards/codes/${c.id}/toggle`, toggleResponse, { body: { disabled } }),
    onSuccess: ({ disabled }) => {
      toast(disabled ? `已停用 ${c.code_masked}` : `已恢复 ${c.code_masked}`)
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      void invalidate()
    },
  })
  const action = c.status === 'unused' ? '停用' : c.status === 'disabled' ? '恢复' : null
  return (
    <div className={local.codeRow}>
      <span className={css.mono}>{c.code_masked}</span>
      <span>
        <Tag tone={status.tone}>{status.label}</Tag>
      </span>
      <span className={`${css.muted} ${local.ellipsis}`}>{c.used_email ?? '—'}</span>
      <span className={local.right}>
        {writable && action && (
          <Button size="xs" variant="link" className={action === '停用' ? local.danger : undefined} busy={toggle.isPending} onClick={() => toggle.mutate(c.status === 'unused')}>
            {action}
          </Button>
        )}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 使用记录
// ---------------------------------------------------------------------------
function Usages() {
  const [templateId, setTemplateId] = useState('')
  const templates = useGiftTemplates()
  const list = useGiftUsages(templateId)
  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Select
          aria-label="按模板筛选"
          size="sm"
          value={templateId}
          onChange={(e) => setTemplateId(e.target.value)}
          options={[{ value: '', label: '全部模板' }, ...(templates.data ?? []).map((t) => ({ value: t.id, label: t.name }))]}
        />
        <span className={css.faint}>最近 200 条</span>
      </div>
      <div className={css.panel}>
        <QueryView query={list} empty={<Empty bare title="还没有兑换记录" description="用户兑换礼品卡后会出现在这里。" />}>
          {(rows) =>
            rows.map((u, i) => (
              <div key={`${u.code_masked}-${u.redeemed_at}-${i}`} className={local.usageRow}>
                <span className={css.mono}>{u.code_masked}</span>
                <span className={local.ellipsis}>{u.user_email}</span>
                <span className={css.muted}>{grantedLabel(u)}</span>
                <span className={`${local.right} ${css.faint}`}>{formatDateTime(u.redeemed_at)}</span>
              </div>
            ))
          }
        </QueryView>
      </div>
    </div>
  )
}
