import { useMutation } from '@tanstack/react-query'
import { useState, type KeyboardEvent } from 'react'
import { saveFile, toCsv } from '../../../core/download'
import { formatMoney } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, FilterTabs, Modal, Pager, QueryView, Switch, Tag, useToast } from '../../../ui'
import { CouponFormModal, type BatchResult } from './CouponForm'
import local from './Coupons.module.css'
import css from './marketing.module.css'
import {
  couponBatchLabel,
  couponPeriodLabel,
  couponState,
  discountLabel,
  formatDateTime,
  planScopeLabel,
  usage,
} from './logic'
import { useCoupons, useFailure, useInvalidateMarketing, usePlanCatalog, useRedemptions } from './queries'
import { okResponse, type Coupon, type Plan } from './schemas'

const PAGE = 50
type Mode = 'all' | 'active' | 'paused'

export function Coupons({ rest }: { rest: string[] }) {
  const toast = useToast()
  const loc = useHashLocation()
  const selectedId = rest[0] ?? null

  const statusParam = loc.query.get('status')
  const mode: Mode = statusParam === 'active' || statusParam === 'paused' ? statusParam : 'all'

  const [offset, setOffset] = useState(0)
  const [formModal, setFormModal] = useState<null | 'one' | 'batch'>(null)
  const [batch, setBatch] = useState<BatchResult | null>(null)

  const list = useCoupons({ ...(mode === 'all' ? {} : { status: mode }), limit: PAGE, offset })
  const catalog = usePlanCatalog()
  const plans = catalog.isSuccess ? catalog.data : undefined

  const pickMode = (next: Mode) => {
    setOffset(0)
    navigate('/marketing/coupons', {
      query: next === 'all' ? {} : { status: next },
      replace: true,
    })
  }

  const openDetail = (id: string) => {
    navigate(`/marketing/coupons/${id}`, {
      query: mode === 'all' ? {} : { status: mode },
    })
  }

  const closeDetail = () => {
    navigate('/marketing/coupons', {
      query: mode === 'all' ? {} : { status: mode },
    })
  }

  const selectedCoupon = list.data?.coupons.find((c) => c.id === selectedId) ?? null

  return (
    <div className={local.container}>
      <div className={local.toolbar}>
        <FilterTabs<Mode>
          label="按状态筛选"
          value={mode}
          onChange={pickMode}
          items={[
            { value: 'all', label: '全部' },
            { value: 'active', label: '启用中' },
            { value: 'paused', label: '已停用' },
          ]}
        />
        <span className={local.spacer} />
        <Button onClick={() => setFormModal('batch')}>批量生成</Button>
        <Button variant="primary" onClick={() => setFormModal('one')}>
          新建
        </Button>
      </div>

      <div className={local.tableWrap}>
        <div className={local.tableInner}>
          <div className={`${local.grid} ${local.head}`} role="row">
            <span>优惠码</span>
            <span>优惠</span>
            <span>适用套餐</span>
            <span className={local.right}>使用</span>
            <span>有效期</span>
            <span className={local.right}>启用</span>
          </div>

          <QueryView
            query={list}
            isEmpty={(d) => d.coupons.length === 0}
            empty={
              <div className={local.empty}>
                {mode === 'all' ? '还没有优惠券' : mode === 'active' ? '没有启用中的优惠券' : '没有已停用的优惠券'}
              </div>
            }
          >
            {(data) => (
              <>
                {data.coupons.map((c) => (
                  <CouponRow
                    key={c.id}
                    coupon={c}
                    plans={plans}
                    onClick={() => openDetail(c.id)}
                  />
                ))}
                <Pager total={data.total} limit={PAGE} offset={offset} onChange={setOffset} />
              </>
            )}
          </QueryView>
        </div>
      </div>

      <CouponFormModal
        open={formModal}
        plans={plans}
        onClose={() => setFormModal(null)}
        onCreated={(result) => {
          setFormModal(null)
          if ('codes' in result) setBatch(result)
          else toast(`优惠券 ${result.code} 已创建`)
        }}
      />

      <CouponDetailModal
        id={selectedId}
        coupon={selectedCoupon}
        plans={plans}
        onClose={closeDetail}
      />

      <BatchResultModal result={batch} onClose={() => setBatch(null)} />
    </div>
  )
}

function CouponRow({
  coupon: c,
  plans,
  onClick,
}: {
  coupon: Coupon
  plans: readonly Plan[] | undefined
  onClick: () => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const state = couponState(c.status)
  const use = usage(c)
  const batchLabel = couponBatchLabel(c)
  const period = couponPeriodLabel(c, new Date())

  const toggle = useMutation({
    mutationFn: (next: 'active' | 'paused') => api.post(`v1/coupons/${c.id}/status`, okResponse, { body: { status: next } }),
    onSuccess: (_, next) => {
      toast(next === 'active' ? `${c.code} 已启用` : `${c.code} 已停用`)
      void invalidate()
    },
    onError: (error) => fail(error),
  })

  // 行里还有开关按钮，所以行本身不能是 <button>（按钮不能嵌套）：用 role="button" 并只响应行自己的按键
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onClick()
    }
  }

  return (
    <div role="button" tabIndex={0} aria-label={`查看 ${c.code}`} className={local.row} onClick={onClick} onKeyDown={onKeyDown}>
      <div className={local.grid}>
        <span className={local.codeCell}>
          <span className={local.codeText}>{c.code}</span>
          {batchLabel && <span className={local.nameText}>{batchLabel}</span>}
        </span>
        <span className={local.offText}>{discountLabel(c)}</span>
        <span className={local.planText}>{planScopeLabel(c.applicable_plan_ids, plans)}</span>
        <span className={local.monoRight}>{use.label}</span>
        <span title={period.full} className={local.periodText}>
          {period.short}
        </span>
        <span
          className={local.switchCell}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
        >
          {state.switchable ? (
            <Switch
              aria-label={`${state.on ? '停用' : '启用'} ${c.code}`}
              checked={toggle.isPending ? toggle.variables === 'active' : state.on}
              disabled={toggle.isPending}
              onChange={() => toggle.mutate(state.on ? 'paused' : 'active')}
            />
          ) : (
            state.tag && <Tag tone={state.tag.tone}>{state.tag.label}</Tag>
          )}
        </span>
      </div>
    </div>
  )
}

function CouponDetailModal({
  id,
  coupon,
  plans,
  onClose,
}: {
  id: string | null
  coupon: Coupon | null
  plans: readonly Plan[] | undefined
  onClose: () => void
}) {
  const redemptions = useRedemptions(id)

  if (!id) return null

  if (!coupon) {
    return (
      <Modal
        open={true}
        onClose={onClose}
        size={520}
        title="优惠券详情"
        actions={
          <Button size="dialog" onClick={onClose}>
            关闭
          </Button>
        }
      >
        <div className={local.empty}>这张券不在当前列表里</div>
      </Modal>
    )
  }

  const use = usage(coupon)
  const period = couponPeriodLabel(coupon, new Date())

  return (
    <Modal
      open={true}
      onClose={onClose}
      size={520}
      title={coupon.code}
      subtitle={coupon.name !== coupon.code ? coupon.name : undefined}
      actions={
        <Button size="dialog" onClick={onClose}>
          关闭
        </Button>
      }
    >
      <div className={css.stack}>
        <div className={local.section}>规则</div>
        <div className={local.summaryRow}>
          <span className={local.summaryLabel}>优惠</span>
          <span className={local.summaryValue}>{discountLabel(coupon)}</span>
        </div>
        <div className={local.summaryRow}>
          <span className={local.summaryLabel}>适用套餐</span>
          <span className={local.summaryValue}>{planScopeLabel(coupon.applicable_plan_ids, plans)}</span>
        </div>
        <div className={local.summaryRow}>
          <span className={local.summaryLabel}>使用</span>
          <span className={`${local.summaryValue} ${local.summaryValueMono}`}>{use.label}</span>
        </div>
        <div className={local.summaryRow}>
          <span className={local.summaryLabel}>有效期</span>
          <span className={`${local.summaryValue} ${local.summaryValueMono}`}>{period.full}</span>
        </div>

        <div className={local.section}>兑换记录（最近 200 条）</div>
        <QueryView
          query={redemptions}
          rows={3}
          empty={<div className={local.logNote}>还没有人用过这张券。</div>}
        >
          {(rows) =>
            rows.map((r, i) => (
              <div key={`${r.order_no}-${r.at}-${i}`} className={local.summaryRow}>
                <span className={local.summaryLabel}>{r.email}</span>
                <span className={local.summaryValueMuted}>
                  <span className={r.reverted ? local.reverted : undefined}>−{formatMoney(r.discount)}</span>
                  {r.reverted && ' 已回滚'}
                  {' · '}
                  {formatDateTime(r.at)}
                </span>
              </div>
            ))
          }
        </QueryView>
      </div>
    </Modal>
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
            {result.codes.length > 12 && <span className={local.faint}>… 共 {result.codes.length} 张，完整清单请下载</span>}
          </div>
        </div>
      )}
    </Modal>
  )
}
