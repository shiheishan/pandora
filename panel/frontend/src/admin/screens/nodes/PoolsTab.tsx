import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Empty, Input, Modal, QueryView, Select, Tag, useToast } from '../../../ui'
import { PageActions } from '../../kit'
import { UK, useUserGroups } from '../users/api'
import { assignableGroups, groupOptionLabel } from '../users/model'
import { createPoolBody, emptyPoolForm, patchPoolBody, planNamesLabel, POOL_STATUS, poolDeleteBlock, poolFormFrom, poolGroupsChanged, poolGroupsLabel, type PoolForm } from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useFailure, useInvalidateNodes, usePools } from './queries'
import { okResponse, poolCreated, POOL_STATUSES, type Pool, type PoolStatus } from './schemas'

export function PoolsTab() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const pools = usePools()
  const [editing, setEditing] = useState<Pool | 'new' | null>(null)
  const [removing, setRemoving] = useState<Pool | null>(null)

  const remove = useMutation({
    mutationFn: (p: Pool) => api.delete(`v1/node-pools/${p.id}`, okResponse).then(() => p),
    onSuccess: (p) => {
      toast(`节点池「${p.name}」已删除`)
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      void invalidate()
    },
  })

  return (
    <div className={css.stack}>
      <PageActions>
        <Button variant="primary" icon={PlusSignIcon} onClick={() => setEditing('new')}>
          新建节点池
        </Button>
      </PageActions>
      <div className={x.bar}>
        <span>{pools.data ? `${pools.data.length} 个节点池 · 节点归入池，套餐绑定池` : '节点池'}</span>
      </div>

      <QueryView query={pools} rows={4} isEmpty={(d) => d.length === 0} empty={<Empty title="还没有节点池" description="新建一个节点池，再在节点里选它、在套餐里绑定它。" />}>
        {(list) => (
          <div className={x.cards}>
            {list.map((p) => {
              const block = poolDeleteBlock(p)
              const groups = poolGroupsLabel(p)
              return (
                <section key={p.id} className={x.card} aria-label={`节点池 ${p.name}`}>
                  <div className={x.ch}>
                    <b>{p.name}</b>
                    <span className={css.faint}>{p.nodes} 个节点</span>
                    {p.status !== 'active' && <Tag tone={POOL_STATUS[p.status].tone}>{POOL_STATUS[p.status].label}</Tag>}
                  </div>
                  <div className={x.cs}>
                    <span className={css.mono}>{p.code}</span>
                    {p.region ? ` · ${p.region}` : ''}
                  </div>
                  <div className={x.chips}>
                    {p.members.length === 0 ? (
                      <span className={css.faint}>没有节点。在节点详情『路由与流量 › 访问』里把节点池选成它。</span>
                    ) : (
                      p.members.map((m) => (
                        <button key={m.id} type="button" className={`${x.memberChip} ${x.chipButton}`} onClick={() => navigate(`/nodes/nodes/${m.id}`)} aria-label={`打开节点 ${m.name}`}>
                          {m.name}
                        </button>
                      ))
                    )}
                  </div>
                  <div className={x.kv2}>
                    <span>绑定套餐</span>
                    <span>
                      {p.plan_names.length > 0 ? (
                        planNamesLabel(p)
                      ) : (
                        <span className={x.note}>没有绑定套餐，暂时不服务任何用户</span>
                      )}
                      {groups && (
                        <>
                          {' '}
                          <Tag tone="info">{groups}</Tag>
                        </>
                      )}
                    </span>
                  </div>
                  <div className={x.cf}>
                    <span className={css.spacer} />
                    <Button size="sm" variant="secondary" onClick={() => setEditing(p)}>
                      编辑
                    </Button>
                    <Button size="sm" variant="secondary" disabled={block !== null || remove.isPending} title={block ?? undefined} onClick={() => setRemoving(p)}>
                      删除
                    </Button>
                  </div>
                </section>
              )
            })}
          </div>
        )}
      </QueryView>

      {editing !== null && <PoolModal pool={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      <ConfirmModal
        open={removing !== null}
        title={`删除节点池「${removing?.name ?? ''}」？`}
        body="删除后不能恢复。"
        confirmLabel="删除节点池"
        tone="danger"
        onConfirm={() => {
          if (removing) remove.mutate(removing)
          setRemoving(null)
        }}
        onCancel={() => setRemoving(null)}
      />
    </div>
  )
}

function PoolModal({ pool, onClose }: { pool: Pool | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const queryClient = useQueryClient()
  const [form, setForm] = useState<PoolForm>(() => (pool ? poolFormFrom(pool) : emptyPoolForm()))
  const [errors, setErrors] = useState<Record<string, string>>({})
  // 这次保存会不会动名单：动了就要 reauth，按钮旁先说
  const groupsTouched = pool ? poolGroupsChanged(pool, form) : form.groupIds.length > 0

  const save = useMutation({
    mutationFn: async (body: Record<string, unknown>) => {
      if (pool) await api.post(`v1/node-pools/${pool.id}`, okResponse, { body })
      else await api.post('v1/node-pools', poolCreated, { body })
    },
    onSuccess: () => {
      toast(pool ? '节点池已保存' : '节点池已创建')
      void invalidate()
      // 用户组列表的「可用节点池」来自这份名单
      void queryClient.invalidateQueries({ queryKey: [...UK, 'groups'] })
      onClose()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = () => {
    if (!form.name.trim()) return setErrors({ name: '名称必填' })
    setErrors({})
    if (!pool) return save.mutate(createPoolBody(form))
    const body = patchPoolBody(pool, form)
    if (!body) return onClose()
    save.mutate(body)
  }

  return (
    <Modal
      open
      onClose={onClose}
      size={440}
      title={pool ? '编辑节点池' : '新建节点池'}
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={save.isPending} onClick={submit}>
            保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <div className={css.grid2}>
          <Input label="名称" value={form.name} error={errors.name} disabled={save.isPending} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="如 亚太精选" />
          {pool ? (
            <Input label="标识" mono value={form.code} disabled readOnly />
          ) : (
            <Input label="标识（可选）" mono value={form.code} error={errors.code} disabled={save.isPending} onChange={(e) => setForm({ ...form, code: e.target.value })} placeholder="留空由名称生成" />
          )}
          <Input label="备注（可选）" value={form.region} disabled={save.isPending} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="如 APAC" />
          {pool && <Select label="状态" value={form.status} disabled={save.isPending} onChange={(e) => setForm({ ...form, status: e.target.value as PoolStatus })} options={POOL_STATUSES.map((v) => ({ value: v, label: POOL_STATUS[v].label }))} />}
        </div>
        <PoolGroups
          picked={form.groupIds}
          disabled={save.isPending}
          error={errors.allowed_user_group_ids}
          onChange={(ids) => setForm({ ...form, groupIds: ids })}
        />
        {groupsTouched && <div className={css.faint}>改动仅限用户组需要重新验证身份。</div>}
      </div>
    </Modal>
  )
}

/**
 * R104「仅限用户组」多选：不选 = 所有买了绑定套餐的用户都能用；选了 = 只有这些组的用户能用，默认组（未分组）的用户用不了。
 */
function PoolGroups({ picked, disabled, error, onChange }: { picked: readonly string[]; disabled: boolean; error?: string; onChange: (ids: string[]) => void }) {
  const groups = useUserGroups()
  const toggle = (id: string) => onChange(picked.includes(id) ? picked.filter((g) => g !== id) : [...picked, id])
  // 停用的组隐藏；打开时已勾选的与当前勾选的照样显示并标「（已停用）」（误点取消后还能勾回来）
  const [initial] = useState(() => [...picked])
  const shown = assignableGroups(groups.data ?? [], [...initial, ...picked])
  return (
    <fieldset className={`${css.stack} ${x.fieldset}`}>
      <legend className={css.faint}>仅限用户组（不选 = 不限定）</legend>
      {groups.isPending ? (
        <span className={css.faint}>正在读取用户组…</span>
      ) : groups.isError ? (
        <span className={css.faint}>用户组读取失败，稍后再试。</span>
      ) : shown.length === 0 ? (
        <span className={css.faint}>还没有可选的用户组，先到「用户 · 用户组」建一个或启用。</span>
      ) : (
        <div className={x.groupChecks}>
          {shown.map((g) => (
            <Checkbox key={g.id} label={groupOptionLabel(g)} checked={picked.includes(g.id)} disabled={disabled} onChange={() => toggle(g.id)} />
          ))}
        </div>
      )}
      {error && (
        <span className={x.fieldError} role="alert">
          {error}
        </span>
      )}
    </fieldset>
  )
}
