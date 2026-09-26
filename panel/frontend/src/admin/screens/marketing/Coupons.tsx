/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/download 的 saveFile / toCsv，依赖 ../../../core/format 的 formatMoney，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./CouponForm、./logic、./queries、./schemas，依赖 ./marketing.module.css 与 ./Coupons.module.css
 * [OUTPUT]: 对外提供 Coupons（营销 · 优惠券标签）
 * [POS]: admin/screens/marketing 的优惠券标签（设计稿 t_coupons）：分段「全部 / 启用中 / 已停用」→ GET v1/coupons 的 status；行内开关 POST v1/coupons/{id}/status（reauth，由常驻对话框接管）；点码展开兑换记录（另需 billing.order.read）；批量生成成功后给出码清单与 CSV 下载（优惠码不是等价现金，没有一次性可见的要求）。契约写明没有编辑入口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { saveFile, toCsv } from '../../../core/download'
import { formatMoney } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, Empty, IconChevronDown, Modal, Pager, QueryView, Segmented, Switch, Tag, useToast } from '../../../ui'
import { CouponForm, type BatchResult } from './CouponForm'
import local from './Coupons.module.css'
import { couponBatchLabel, couponState, discountLabel, formatDateTime, planScopeLabel, usage, validUntilLabel } from './logic'
import css from './marketing.module.css'
import { useCan, useCoupons, useFailure, useInvalidateMarketing, usePlanCatalog, useRedemptions } from './queries'
import { okResponse, type Coupon, type Plan } from './schemas'

const PAGE = 50
type Mode = 'all' | 'active' | 'paused'

export function Coupons() {
  const can = useCan()
  const toast = useToast()
  const [mode, setMode] = useState<Mode>('all')
  const [offset, setOffset] = useState(0)
  const [form, setForm] = useState<null | 'one' | 'batch'>(null)
  const [open, setOpen] = useState<string | null>(null)
  const [batch, setBatch] = useState<BatchResult | null>(null)
  const list = useCoupons({ ...(mode === 'all' ? {} : { status: mode }), limit: PAGE, offset })
  const catalog = usePlanCatalog()
  const plans = catalog.isSuccess ? catalog.data : undefined
  const writable = can('marketing.coupon.write')
  const withLog = can('billing.order.read')

  const pickMode = (next: Mode) => {
    setMode(next)
    setOffset(0)
    setOpen(null)
  }

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <Segmented<Mode>
          label="按状态筛选"
          size="sm"
          value={mode}
          onChange={pickMode}
          options={[
            { value: 'all', label: '全部' },
            { value: 'active', label: '启用中' },
            { value: 'paused', label: '已停用' },
          ]}
        />
        <span className={css.spacer} />
        {writable && (
          <>
            <Button onClick={() => setForm(form === 'batch' ? null : 'batch')}>批量生成</Button>
            <Button variant="primary" onClick={() => setForm(form === 'one' ? null : 'one')}>
              新建优惠券
            </Button>
          </>
        )}
      </div>

      {form && (
        <CouponForm
          key={form}
          batch={form === 'batch'}
          plans={plans}
          onCancel={() => setForm(null)}
          onCreated={(result) => {
            setForm(null)
            if ('codes' in result) setBatch(result)
            else toast(`优惠券 ${result.code} 已创建`)
          }}
        />
      )}

      <div className={css.panel}>
        <div className={`${local.grid} ${local.head}`} role="row">
          <span>优惠码</span>
          <span>优惠</span>
          <span>适用套餐</span>
          <span>使用</span>
          <span>有效期至</span>
          <span className={local.right}>启用</span>
        </div>
        <QueryView
          query={list}
          isEmpty={(d) => d.coupons.length === 0}
          empty={
            <Empty
              bare
              title={mode === 'all' ? '还没有优惠券' : mode === 'active' ? '没有启用中的优惠券' : '没有已停用的优惠券'}
              description={writable ? '用「新建优惠券」发一张，或「批量生成」给渠道发一批。' : '有营销写权限的同事可以在这里创建。'}
            />
          }
        >
          {(data) => (
            <>
              {data.coupons.map((c) => (
                <CouponRow key={c.id} coupon={c} plans={plans} writable={writable} withLog={withLog} open={open === c.id} onToggleOpen={() => setOpen(open === c.id ? null : c.id)} />
              ))}
              <Pager total={data.total} limit={PAGE} offset={offset} onChange={setOffset} />
            </>
          )}
        </QueryView>
      </div>

      <BatchResultModal result={batch} onClose={() => setBatch(null)} />
    </div>
  )
}

function CouponRow({
  coupon: c,
  plans,
  writable,
  withLog,
  open,
  onToggleOpen,
}: {
  coupon: Coupon
  plans: readonly Plan[] | undefined
  writable: boolean
  withLog: boolean
  open: boolean
  onToggleOpen: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const state = couponState(c.status)
  const use = usage(c)
  const batch = couponBatchLabel(c)

  const toggle = useMutation({
    mutationFn: (next: 'active' | 'paused') => api.post(`v1/coupons/${c.id}/status`, okResponse, { body: { status: next } }),
    onSuccess: (_, next) => {
      toast(next === 'active' ? `已启用 ${c.code}` : `已停用 ${c.code}`)
      void invalidate()
    },
    onError: (error) => fail(error),
  })

  const codeCell = (
    <>
      <span className={local.codeText}>{c.code}</span>
      {batch && <span className={css.faint}>{batch}</span>}
      {withLog && <IconChevronDown className={local.chevron} />}
    </>
  )

  return (
    <div className={open ? `${local.row} ${local.open}` : local.row}>
      <div className={`${local.grid} ${local.line}`}>
        {withLog ? (
          <button type="button" className={local.code} aria-expanded={open} onClick={onToggleOpen}>
            {codeCell}
          </button>
        ) : (
          <span className={local.code}>{codeCell}</span>
        )}
        <span>{discountLabel(c)}</span>
        <span className={css.muted}>{planScopeLabel(c.applicable_plan_ids, plans)}</span>
        <span className={local.usage}>
          <span className={`${css.mono} ${css.faint}`}>{use.label}</span>
          <span className={css.bar}>
            <span className={css.barFill} style={{ width: `${Math.round(use.ratio * 100)}%` }} />
          </span>
        </span>
        <span className={`${css.mono} ${css.faint}`}>{validUntilLabel(c.valid_until)}</span>
        <span className={local.end}>
          {state.switchable ? (
            <Switch
              aria-label={`${state.on ? '停用' : '启用'} ${c.code}`}
              checked={toggle.isPending ? toggle.variables === 'active' : state.on}
              disabled={!writable || toggle.isPending}
              onChange={() => toggle.mutate(state.on ? 'paused' : 'active')}
            />
          ) : (
            state.tag && <Tag tone={state.tag.tone}>{state.tag.label}</Tag>
          )}
        </span>
      </div>
      {open && <Redemptions couponId={c.id} />}
    </div>
  )
}

function Redemptions({ couponId }: { couponId: string }) {
  const log = useRedemptions(couponId)
  return (
    <div className={local.log}>
      <div className={local.logTitle}>兑换记录（最近 200 条）</div>
      <QueryView query={log} rows={2} empty={<div className={css.faint}>还没有人用过这张券。</div>}>
        {(rows) =>
          rows.map((r) => (
            <div key={`${r.order_no}-${r.at}`} className={local.logRow}>
              <span>{r.email}</span>
              <span className={`${css.mono} ${css.muted}`}>{r.order_no}</span>
              <span className={`${css.mono} ${local.right} ${r.reverted ? local.reverted : ''}`}>
                −{formatMoney(r.discount, r.currency || 'CNY')}
                {r.reverted && ' 已回滚'}
              </span>
              <span className={`${local.right} ${css.faint}`}>{formatDateTime(r.at)}</span>
            </div>
          ))
        }
      </QueryView>
    </div>
  )
}

function BatchResultModal({ result, onClose }: { result: BatchResult | null; onClose: () => void }) {
  const download = () => {
    if (!result) return
    const safe = result.name.replace(/[\\/:*?"<>|\s]+/g, '-')
    saveFile(toCsv([['优惠码'], ...result.codes.map((code) => [code])]), `coupons-${safe}.csv`)
  }
  return (
    <Modal
      open={result !== null}
      onClose={onClose}
      title={result ? `「${result.name}」已生成 ${result.count} 张` : ''}
      actions={
        <>
          <Button size="dialog" onClick={onClose}>
            关闭
          </Button>
          <Button size="dialog" variant="primary" onClick={download} data-autofocus="">
            下载 CSV
          </Button>
        </>
      }
    >
      {result && (
        <div className={css.stack}>
          <div className={css.muted}>列表里这批券的码旁会显示活动名称，按它辨认同一批。</div>
          <div className={local.sample}>
            {result.codes.slice(0, 12).map((code) => (
              <span key={code}>{code}</span>
            ))}
            {result.codes.length > 12 && <span className={css.faint}>… 共 {result.codes.length} 张，完整清单请下载</span>}
          </div>
        </div>
      )}
    </Modal>
  )
}
