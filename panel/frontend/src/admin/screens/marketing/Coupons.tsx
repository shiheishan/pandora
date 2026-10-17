import { useMutation } from '@tanstack/react-query'
import { HugeiconsIcon } from '@hugeicons/react'
import { Add01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useState } from 'react'
import { saveFile, toCsv } from '../../../core/download'
import { formatMoney } from '../../../core/format'
import { navigate, useHashLocation } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, FilterTabs, Modal, QueryView, Switch, Table, Tag, useToast } from '../../../ui'
import { PageActions } from '../../kit'
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

      <QueryView query={list} empty={null}>
        {(data) => {
          const pages = Math.ceil(data.total / PAGE)
          const page = Math.floor(offset / PAGE) + 1
          return (
            <Table<Coupon>
              label="优惠券"
              boxed
              rows={data.coupons}
              rowKey={(c) => c.id}
              onRowClick={(c) => openDetail(c.id)}
              rowClassName={(c) => (couponState(c.status).switchable ? undefined : local.dimRow)}
              empty={
                <div className={local.empty}>
                  {mode === 'all' ? '还没有优惠券' : mode === 'active' ? '没有启用中的优惠券' : '没有已停用的优惠券'}
                </div>
              }
              columns={[
                {
                  key: 'code',
                  header: '优惠码',
                  render: (c) => {
                    const batchLabel = couponBatchLabel(c)
                    return (
                      <span className={local.codeCell}>
                        <span className={local.codeMain}>{c.code}</span>
                        {batchLabel && <span className={local.codeSub}>{batchLabel}</span>}
                      </span>
                    )
                  },
                },
                { key: 'discount', header: '优惠', render: (c) => <span className={local.offCell}>{discountLabel(c)}</span> },
                {
                  key: 'plans',
                  header: '适用套餐',
                  render: (c) => <span className={local.cellDim}>{planScopeLabel(c.applicable_plan_ids, plans)}</span>,
                },
                { key: 'usage', header: '使用', align: 'right', render: (c) => <span className={local.numCell}>{usage(c).label}</span> },
                {
                  key: 'period',
                  header: '有效期',
                  render: (c) => {
                    const period = couponPeriodLabel(c, new Date())
                    return (
                      <span title={period.full} className={local.cellDim}>
                        {period.short}
                      </span>
                    )
                  },
                },
                { key: 'enabled', header: '启用', align: 'right', render: (c) => <CouponToggle coupon={c} /> },
              ]}
              footer={
                <div className={local.footerRow}>
                  <span>共 {data.total} 张</span>
                  {data.total > PAGE && (
                    <>
                      <span className={local.footerSpacer} />
                      <span className={local.pageLabel}>
                        第 {page} / {pages} 页
                      </span>
                      <Button size="sm" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
                        上一页
                      </Button>
                      <Button size="sm" disabled={page >= pages} onClick={() => setOffset(offset + PAGE)}>
                        下一页
                      </Button>
                    </>
                  )}
                </div>
              }
            />
          )
        }}
      </QueryView>

      <PageActions>
        <Button onClick={() => setFormModal('batch')}>批量生成</Button>
        <Button variant="primary" onClick={() => setFormModal('one')}>
          <HugeiconsIcon icon={Add01Icon} size={16} aria-hidden="true" />
          新建
        </Button>
      </PageActions>

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

      <CouponDetailModal id={selectedId} coupon={selectedCoupon} plans={plans} onClose={closeDetail} />

      <BatchResultModal result={batch} onClose={() => setBatch(null)} />
    </div>
  )
}

/** 「启用」列：可切换状态给开关（乐观显示，pending 时禁用），否则给灰标签 */
function CouponToggle({ coupon: c }: { coupon: Coupon }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const state = couponState(c.status)

  const toggle = useMutation({
    mutationFn: (next: 'active' | 'paused') => api.post(`v1/coupons/${c.id}/status`, okResponse, { body: { status: next } }),
    onSuccess: (_, next) => {
      toast(next === 'active' ? `${c.code} 已启用` : `${c.code} 已停用`)
      void invalidate()
    },
    onError: (error) => fail(error),
  })

  // 点开关不要触发整行打开详情
  return (
    <span className={local.toggleCell} onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
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
