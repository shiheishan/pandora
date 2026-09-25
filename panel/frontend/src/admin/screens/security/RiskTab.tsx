/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation / useQueryClient，依赖 ../../../core/format 的 formatDateTime / relativeTime，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的风控函数，依赖 ./queries，依赖 ./schemas，依赖 ./security.module.css
 * [OUTPUT]: 对外提供 RiskTab（安全与运维 · 风控标签）
 * [POS]: admin/screens/security 的风控（设计稿 t_risk）：共享 IP 聚类卡片（IP、归属地 · 网络类型 · 最近时间、后端给的风险徽标、账号 + 当前套餐列表可跳用户详情），「显示已标记正常的」勾选即 include_reviewed=1（R40）。
 *        标记为正常：security.risk.review、无 reauth 无幂等，30 天内不再提示；禁用 N 个账号：security.risk.review + iam.user.write + reauth + 幂等 ip_cluster_disable（R41），弹窗默认全选可停用的成员、原因 5–500 字必填，后台账号由后端跳过。两种处置成功后把结论就地补进缓存，卡片按设计稿留在原位显示结果行，下次重拉（默认列表不含已标记正常的）才消失；停用还让用户模块的查询失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { formatDateTime, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Empty, Modal, QueryView, Tag, TextArea, useToast } from '../../../ui'
import { clusterPlace, disableCandidates, disableSummary, reviewState, reviewText, RISK_LABEL, RISK_TONE, USER_STATUS_LABEL, validateDisable, withDisabled, withReview } from './logic'
import { SK, useCan, useClusters, useFailure, useIntentKey } from './queries'
import { clusterDisabled, clusterReviewed, type Cluster } from './schemas'
import css from './security.module.css'

export function RiskTab() {
  const [includeReviewed, setIncludeReviewed] = useState(false)
  const clusters = useClusters(includeReviewed)
  const [disabling, setDisabling] = useState<Cluster | null>(null)

  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <span className={css.muted}>同一出口 IP 下出现多个账号时聚类展示（近 90 天，至多 50 个），常见于账号共享或批量注册。处置会写入审计。</span>
        <span className={css.spacer} />
        <Checkbox label="显示已标记正常的" checked={includeReviewed} onChange={(e) => setIncludeReviewed(e.target.checked)} />
      </div>
      <QueryView
        query={clusters}
        rows={4}
        isEmpty={(list) => list.length === 0}
        empty={
          <Empty
            title="没有需要处理的共享 IP"
            description={includeReviewed ? '近 90 天没有两个以上账号共用同一个出口 IP。' : '近 90 天没有待处理的聚类；标记为正常的 30 天内不再出现，勾选右上角可以查看。'}
          />
        }
      >
        {(list) => (
          <div className={css.clusters}>
            {list.map((c) => (
              <ClusterCard key={c.key} cluster={c} includeReviewed={includeReviewed} onDisable={() => setDisabling(c)} />
            ))}
          </div>
        )}
      </QueryView>
      {disabling && <DisableModal cluster={disabling} includeReviewed={includeReviewed} onClose={() => setDisabling(null)} />}
    </div>
  )
}

function ClusterCard({ cluster: c, includeReviewed, onDisable }: { cluster: Cluster; includeReviewed: boolean; onDisable: () => void }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const client = useQueryClient()
  const state = reviewState(c.review)
  const result = reviewText(state)
  const candidates = disableCandidates(c)
  const reviewer = can('security.risk.review')
  const canDisable = reviewer && can('iam.user.write') && candidates.length > 0
  const canMark = reviewer && state.kind !== 'normal'
  const linkUsers = can('iam.user.read')

  const mark = useMutation({
    mutationFn: () => api.post(`v1/ip-clusters/${encodeURIComponent(c.key)}/review`, clusterReviewed, { body: {} }),
    onSuccess: (r) => {
      client.setQueryData<Cluster[]>([...SK, 'clusters', includeReviewed], (old) => old && withReview(old, c.key, { decision: 'normal', decided_at: new Date().toISOString(), expires_at: r.expires_at }))
      void client.invalidateQueries({ queryKey: [...SK, 'audit'] })
      toast('已标记为正常，30 天内不再提示')
    },
    onError: (e) => fail(e),
  })

  const handled = state.kind === 'normal' || state.kind === 'disabled'
  const flagged = c.risk === 'high' && !handled
  return (
    <section className={`${css.cluster} ${flagged ? css.flagged : ''} ${handled ? css.handled : ''}`} aria-label={`共享 IP ${c.ip || c.key.slice(0, 8)}`}>
      <div className={css.clusterHead}>
        <div className={css.clusterTitle}>
          <div className={css.clusterIp}>{c.ip || 'IP 无法解密'}</div>
          <div className={css.faint} title={`首次 ${formatDateTime(c.first)} · 最近 ${formatDateTime(c.last)}`}>
            {clusterPlace(c)} · 最近 {relativeTime(c.last)}
          </div>
        </div>
        <Tag tone={RISK_TONE[c.risk]}>{RISK_LABEL[c.risk]}</Tag>
      </div>
      <div className={css.clusterBody}>
        <div className={css.faint}>
          {c.accounts} 个账号 · {c.events} 次事件
          {c.users.length < c.accounts && ` · ${c.accounts - c.users.length} 个账号已不存在`}
        </div>
        <ul className={css.members}>
          {c.users.map((u) => (
            <li key={u.id} className={css.member}>
              {linkUsers ? (
                <a className={css.ellipsis} href={href(`/users/list/${encodeURIComponent(u.id)}`)}>
                  {u.email}
                </a>
              ) : (
                <span className={css.ellipsis}>{u.email}</span>
              )}
              {u.status !== 'active' && <Tag tone={u.status === 'banned' || u.status === 'suspended' ? 'danger' : 'neutral'}>{USER_STATUS_LABEL[u.status] ?? u.status}</Tag>}
              <span className={css.memberPlan}>{u.active_plan ?? '无生效套餐'}</span>
            </li>
          ))}
        </ul>
      </div>
      {(result || canMark || canDisable) && (
        <div className={css.clusterFoot}>
          {result && <span className={css.muted}>{result}</span>}
          {canMark && (
            <Button size="xs" busy={mark.isPending} onClick={() => mark.mutate()}>
              标记为正常
            </Button>
          )}
          <span className={css.spacer} />
          {canDisable && (
            <Button size="xs" variant="danger" onClick={onDisable}>
              禁用 {candidates.length} 个账号
            </Button>
          )}
        </div>
      )}
    </section>
  )
}

function DisableModal({ cluster: c, includeReviewed, onClose }: { cluster: Cluster; includeReviewed: boolean; onClose: () => void }) {
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
      client.setQueryData<Cluster[]>([...SK, 'clusters', includeReviewed], (old) => old && withDisabled(old, c.key, body.user_ids, r))
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
      size="md"
      onClose={() => !run.isPending && onClose()}
      dismissible={!run.isPending}
      eyebrow={c.ip || c.key.slice(0, 8)}
      title={`禁用 ${picked.size} 个账号？`}
      actions={
        <>
          <Button size="dialog" disabled={run.isPending} onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="danger" busy={run.isPending} disabled={picked.size === 0} onClick={submit}>
            禁用
          </Button>
        </>
      }
    >
      <div className={css.form}>
        <p className={css.lead}>账号将被登出、订阅停止下发。这里是「停用」不是「封禁」，误伤了可以在用户页恢复；持有后台角色的账号会被跳过。需要二次认证。</p>
        <fieldset className={css.pickList}>
          <legend className={css.label}>要禁用的账号</legend>
          {candidates.map((u) => (
            <Checkbox key={u.id} label={`${u.email} · ${u.active_plan ?? '无生效套餐'}`} checked={picked.has(u.id)} disabled={run.isPending} onChange={(e) => toggle(u.id, e.target.checked)} />
          ))}
          {errors.user_ids && (
            <div className={css.error} role="alert">
              {errors.user_ids}
            </div>
          )}
        </fieldset>
        <TextArea label="原因" rows={3} value={reason} error={errors.reason} placeholder="5 到 500 字，会写入每个账号的审计记录" disabled={run.isPending} onChange={(e) => setReason(e.target.value)} />
      </div>
    </Modal>
  )
}
