import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Empty, FilterTabs, Modal, QueryView, Tag, TextArea, useToast } from '../../../ui'
import {
  clusterPlace,
  clusterViews,
  disableCandidates,
  disableSummary,
  isMarkedNormal,
  reviewState,
  reviewText,
  RISK_LABEL,
  RISK_TONE,
  USER_STATUS_LABEL,
  validateDisable,
  withDisabled,
  withReview,
  type ClusterView,
} from './logic'
import { DevicePolicy } from '../users/DevicePolicy'
import { SK, useClusters, useFailure, useIntentKey } from './queries'
import { clusterDisabled, clusterReviewed, type Cluster } from './schemas'
import sec from './security.module.css'
import css from './risk.module.css'

const EMPTY_TEXT: Record<ClusterView, string> = {
  // 三段空状态，文案按任务指定
  open: '近 90 天没有待处理的聚类；标记为正常的 30 天内不再出现。',
  done: '没有标记为正常的聚类',
  all: '近 90 天没有两个以上账号共用同一个出口 IP。',
}

export function RiskTab() {
  const [view, setView] = useState<ClusterView>('open')
  // 三段筛选：open 来自默认列表，done / all 来自 include_reviewed=1 的列表；两个查询同时发
  const open = useClusters(false)
  const full = useClusters(true)
  const views = clusterViews(open.data, full.data)
  const [marking, setMarking] = useState<Cluster | null>(null)
  const [disabling, setDisabling] = useState<Cluster | null>(null)

  const items = [
    { value: 'open' as const, label: '待处理', count: views.open?.length },
    { value: 'done' as const, label: '已标记正常', count: views.done?.length },
    { value: 'all' as const, label: '全部', count: views.all?.length },
  ]

  // 当前段的数据来源：open 段看默认列表，done / all 段看含已标记正常的列表
  const current = view === 'open' ? open : full

  return (
    <div className={sec.stack}>
      <FilterTabs label="按处置状态筛选" items={items} value={view} onChange={setView} />
      <QueryView
        query={current}
        rows={4}
        isEmpty={(list) => list.length === 0}
        empty={<Empty bare title={EMPTY_TEXT[view]} />}
      >
        {() => {
          const list = views[view]
          // done 段：含已标记正常的列表到了但里面没有有效的「标记正常」，同样给空状态
          if (!list || list.length === 0) {
            return <Empty bare title={EMPTY_TEXT[view]} />
          }
          return (
            <div className={css.list}>
              {list.map((c) => (
                <ClusterRow key={c.key} cluster={c} onMark={() => setMarking(c)} onDisable={() => setDisabling(c)} />
              ))}
            </div>
          )
        }}
      </QueryView>
      {marking && <MarkConfirmModal cluster={marking} onClose={() => setMarking(null)} />}
      {disabling && <DisableModal cluster={disabling} onClose={() => setDisabling(null)} />}
      <div className={css.deviceSlot}>
        <DevicePolicy />
      </div>
    </div>
  )
}

/** 「标记正常」先确认再提交（POST v1/ip-clusters/{key}/review），成功后补两个缓存键 */
function MarkConfirmModal({ cluster: c, onClose }: { cluster: Cluster; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const client = useQueryClient()

  const mark = useMutation({
    mutationFn: () => api.post(`v1/ip-clusters/${encodeURIComponent(c.key)}/review`, clusterReviewed, { body: {} }),
    onSuccess: (r) => {
      const review = { decision: 'normal' as const, decided_at: new Date().toISOString(), expires_at: r.expires_at }
      client.setQueryData<Cluster[]>([...SK, 'clusters', false], (old) => old && withReview(old, c.key, review))
      client.setQueryData<Cluster[]>([...SK, 'clusters', true], (old) => old && withReview(old, c.key, review))
      // 待处理段按后端默认列表算，重拉后这一条移到「已标记正常」
      void client.invalidateQueries({ queryKey: [...SK, 'clusters'] })
      void client.invalidateQueries({ queryKey: [...SK, 'audit'] })
      toast('已标记为正常，30 天内不再提示')
    },
    onError: (e) => fail(e),
  })

  // 失败时弹窗留着可重试（reauth、断网由外框与 useFailure 接管）
  const confirm = async () => {
    try {
      await mark.mutateAsync()
      onClose()
    } catch {
      /* 已由 useFailure 提示 */
    }
  }

  return (
    <ConfirmModal
      open
      title={`把 ${c.ip || 'IP 无法解密'} 标记为正常？`}
      body="30 天内不再提示这个聚类；之后出现新账号会重新提示。"
      confirmLabel="标记正常"
      onConfirm={confirm}
      onCancel={onClose}
    />
  )
}

/** 成员状态字的颜色：停用 / 封禁红、待验证橙、其余灰 */
function statusClass(status: string): string | undefined {
  if (status === 'suspended' || status === 'banned') return css.stDanger
  if (status === 'pending') return css.stWarn
  return css.stNormal
}

function ClusterRow({ cluster: c, onMark, onDisable }: { cluster: Cluster; onMark: () => void; onDisable: () => void }) {
  const state = reviewState(c.review)
  const result = reviewText(state)
  const candidates = disableCandidates(c)
  const canMark = !isMarkedNormal(c)

  return (
    <div className={`${css.row} ${isMarkedNormal(c) ? css.dim : ''}`}>
      <div className={css.info}>
        <span className={css.ip}>{c.ip || 'IP 无法解密'}</span>
        <span className={css.place}>{clusterPlace(c)}</span>
        <span className={css.meta}>
          <Tag tone={RISK_TONE[c.risk]}>{RISK_LABEL[c.risk]}</Tag>
          <span className={css.count}>{c.accounts} 个账号</span>
        </span>
        {result && <span className={css.note}>{result}</span>}
      </div>
      <div className={css.members}>
        {c.users.map((u) => (
          <div key={u.id} className={css.member}>
            <a className={sec.ellipsis} href={href(`/users/list/${encodeURIComponent(u.id)}`)}>
              {u.email}
            </a>
            <span className={`${css.status} ${statusClass(u.status)}`}>{USER_STATUS_LABEL[u.status] ?? u.status}</span>
            <span className={css.plan}>{u.active_plan ?? '无生效套餐'}</span>
          </div>
        ))}
        {c.users.length < c.accounts && <div className={css.more}>另有 {c.accounts - c.users.length} 个账号已不存在</div>}
      </div>
      <div className={css.actions}>
        {canMark && (
          <button type="button" className={css.textBtn} onClick={onMark}>
            标记正常
          </button>
        )}
        {candidates.length > 0 && (
          <button type="button" className={`${css.textBtn} ${css.dangerBtn}`} onClick={onDisable}>
            禁用账号…
          </button>
        )}
      </div>
    </div>
  )
}

/** 勾选成员、原因 5–500 字、幂等键；reauth 由外框对话框接管。成功后同时补两个缓存键 */
function DisableModal({ cluster: c, onClose }: { cluster: Cluster; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const client = useQueryClient()
  const candidates = disableCandidates(c)
  const [picked, setPicked] = useState<ReadonlySet<string>>(() => new Set(candidates.map((u) => u.id)))
  const [reason, setReason] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})

  const run = useMutation({
    mutationFn: (body: { user_ids: string[]; reason: string }) =>
      api.post(`v1/ip-clusters/${encodeURIComponent(c.key)}/disable-accounts`, clusterDisabled, { body, idempotencyKey: intent.keyFor([c.key, body]) }),
    onSuccess: (r, body) => {
      intent.reset()
      client.setQueryData<Cluster[]>([...SK, 'clusters', false], (old) => old && withDisabled(old, c.key, body.user_ids, r))
      client.setQueryData<Cluster[]>([...SK, 'clusters', true], (old) => old && withDisabled(old, c.key, body.user_ids, r))
      void client.invalidateQueries({ queryKey: [...SK, 'audit'] })
      void client.invalidateQueries({ queryKey: ['admin', 'users'] })
      const summary = disableSummary(r)
      toast(summary.message, summary.ok ? 'ok' : 'danger')
      onClose()
    },
    onError: (e) => fail(e, { fields: setErrors, intent }),
  })

  const submit = () => {
    const ids = candidates.filter((u) => picked.has(u.id)).map((u) => u.id)
    const errs = validateDisable(ids, reason)
    setErrors(errs)
    if (Object.keys(errs).length === 0) run.mutate({ user_ids: ids, reason: reason.trim() })
  }
  const toggle = (id: string, on: boolean) => {
    const next = new Set(picked)
    if (on) next.add(id)
    else next.delete(id)
    setPicked(next)
  }

  return (
    <Modal
      open
      size={520}
      onClose={() => !run.isPending && onClose()}
      dismissible={!run.isPending}
      title={`禁用 ${c.ip || 'IP 无法解密'} 下的账号`}
      actions={
        <>
          <Button size="dialog" disabled={run.isPending} onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="danger" busy={run.isPending} disabled={picked.size === 0} onClick={submit}>
            禁用账号
          </Button>
        </>
      }
    >
      <div className={sec.form}>
        <p className={sec.lead}>账号将被登出、订阅停止下发。这里是「停用」不是「封禁」，误伤了可以在用户页恢复；持有后台角色的账号会被跳过。需要二次认证。</p>
        <fieldset className={sec.pickList}>
          <legend className={sec.label}>要禁用的账号</legend>
          {candidates.map((u) => (
            <Checkbox key={u.id} label={`${u.email} · ${u.active_plan ?? '无生效套餐'}`} checked={picked.has(u.id)} disabled={run.isPending} onChange={(e) => toggle(u.id, e.target.checked)} />
          ))}
          {errors.user_ids && (
            <div className={sec.error} role="alert">
              {errors.user_ids}
            </div>
          )}
        </fieldset>
        <TextArea label="原因" rows={3} value={reason} error={errors.reason} placeholder="5 到 500 字，会写入每个账号的审计记录" disabled={run.isPending} onChange={(e) => setReason(e.target.value)} />
      </div>
    </Modal>
  )
}
