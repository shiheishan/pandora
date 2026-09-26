/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation / useQueryClient，依赖 ../../../core/router 的 navigate，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./infra 的节点池纯函数，依赖 ./queries、./schemas，依赖 ../users/api 的 useUserGroups 与 UK（用户组候选、存后失效用户组列表），依赖 ./nodes.module.css 与 ./infra.module.css
 * [OUTPUT]: 对外提供 PoolsTab（节点与服务器 · 节点池标签）
 * [POS]: admin/screens/nodes 的节点池（设计稿 t_pools）：三栏卡片（名称 · 节点数 · 状态 | 组内节点标签，点标签跳节点抽屉 | 绑定套餐名「、」连接，限定了用户组时追加「仅用户组『…』」）；设计缺、契约待补·前端的新建 / 编辑 / 删除都补上：新建 POST v1/node-pools（code 留空由名字派生，回 200 { id }），编辑 POST v1/node-pools/{id}（空串 = 不改，名称与地区清不空），删除在有节点或套餐时直接禁用并说明，其余阻碍（节点模板、发布记录、未用令牌）靠 409 文案。R104「仅限用户组」：弹窗里多选（要 iam.user.read 才列得出组名），名单变了才带 allowed_user_group_ids，带了就要 reauth（由外框对话框接管，弹窗里先说明）；存后连用户组列表的「可用节点池」一起失效；节点归属不在这里改，走节点编辑的资源池
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { navigate } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Empty, Input, Modal, QueryView, Select, Tag, useToast } from '../../../ui'
import { UK, useUserGroups } from '../users/api'
import { createPoolBody, emptyPoolForm, patchPoolBody, planNamesLabel, POOL_STATUS, poolDeleteBlock, poolFormFrom, poolGroupsChanged, poolGroupsLabel, type PoolForm } from './infra'
import x from './infra.module.css'
import css from './nodes.module.css'
import { useCan, useFailure, useInvalidateNodes, usePools } from './queries'
import { okResponse, poolCreated, POOL_STATUSES, type Pool, type PoolStatus } from './schemas'

export function PoolsTab() {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const pools = usePools()
  const [editing, setEditing] = useState<Pool | 'new' | null>(null)
  const [removing, setRemoving] = useState<Pool | null>(null)
  const writable = can('node.provision')

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
      <div className={x.bar}>
        <span>节点池决定「谁能看到哪些节点」：节点归入节点池，套餐绑定节点池。</span>
        <span className={css.spacer} />
        {writable && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            新建节点池
          </Button>
        )}
      </div>

      <QueryView query={pools} rows={4} isEmpty={(d) => d.length === 0} empty={<Empty title="还没有节点池" description={writable ? '新建一个节点池，再在节点里选它、在套餐里绑定它。' : '有节点部署权限的同事可以在这里新建节点池。'} />}>
        {(list) => (
          <div className={x.pools}>
            {list.map((p) => {
              const block = poolDeleteBlock(p)
              return (
                <section key={p.id} className={x.pool} aria-label={`节点池 ${p.name}`}>
                  <div>
                    <div className={x.poolName}>{p.name}</div>
                    <div className={x.poolMeta}>
                      <span>{p.nodes} 个节点</span>
                      {p.status !== 'active' && <Tag tone={POOL_STATUS[p.status].tone}>{POOL_STATUS[p.status].label}</Tag>}
                    </div>
                    <div className={x.poolMeta}>
                      <span className={css.mono}>{p.code}</span>
                      {p.region && <span>· {p.region}</span>}
                    </div>
                    {writable && (
                      <div className={x.poolActions}>
                        <Button size="xs" onClick={() => setEditing(p)}>
                          编辑
                        </Button>
                        <Button size="xs" variant="ghost" className={block ? undefined : x.dangerText} disabled={block !== null || remove.isPending} title={block ?? undefined} onClick={() => setRemoving(p)}>
                          删除
                        </Button>
                      </div>
                    )}
                  </div>
                  <div className={x.chips}>
                    {p.members.length === 0 && <span className={css.faint}>没有节点。在节点的「协议参数」里把资源池选成它。</span>}
                    {p.members.map((m) => (
                      <button key={m.id} type="button" className={`${x.memberChip} ${x.chipButton}`} onClick={() => navigate(`/nodes/nodes/${m.id}`)} aria-label={`打开节点 ${m.name}`}>
                        {m.name}
                      </button>
                    ))}
                  </div>
                  <div className={x.poolPlans}>
                    <span className={css.faint}>绑定套餐</span>
                    <span>{planNamesLabel(p)}</span>
                    {poolGroupsLabel(p) && <Tag tone="info">{poolGroupsLabel(p)}</Tag>}
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
        body="删除后不能恢复。节点池下已经没有节点和套餐绑定；如果还有节点模板、配置发布记录或未用的安装令牌引用它，后端会拒绝并说明原因。"
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
    if (!form.name.trim()) return setErrors({ name: '分组名必填' })
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
      size="md"
      title={pool ? `编辑节点池「${pool.name}」` : '新建节点池'}
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={save.isPending} onClick={submit}>
            {pool ? '保存' : '创建'}
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
          <Input label="地区（可选）" value={form.region} disabled={save.isPending} onChange={(e) => setForm({ ...form, region: e.target.value })} placeholder="如 APAC" />
          {pool && <Select label="状态" value={form.status} disabled={save.isPending} onChange={(e) => setForm({ ...form, status: e.target.value as PoolStatus })} options={POOL_STATUSES.map((v) => ({ value: v, label: POOL_STATUS[v].label }))} />}
        </div>
        <div className={css.faint}>{pool ? '标识创建后不能改；名称与地区只能改成别的值，不能清空。' : '标识给接口与脚本用，创建后不能改。新建的节点池一律是启用状态。'}</div>
        <PoolGroups
          picked={form.groupIds}
          current={pool?.allowed_user_groups ?? []}
          disabled={save.isPending}
          error={errors.allowed_user_group_ids}
          onChange={(ids) => setForm({ ...form, groupIds: ids })}
        />
        {groupsTouched && <div className={css.faint}>改动「仅限用户组」会改变谁能用这组节点，保存时需要重新验证身份。</div>}
      </div>
    </Modal>
  )
}

/**
 * R104「仅限用户组」多选：不选 = 所有买了绑定套餐的用户都能用；选了 = 只有这些组的用户能用，默认组（未分组）的用户用不了。
 * 列组名要 iam.user.read；没有就只读显示当前名单
 */
function PoolGroups({ picked, current, disabled, error, onChange }: { picked: readonly string[]; current: ReadonlyArray<{ id: string; name: string }>; disabled: boolean; error?: string; onChange: (ids: string[]) => void }) {
  const can = useCan()
  const readable = can('iam.user.read')
  const groups = useUserGroups(readable)
  const toggle = (id: string) => onChange(picked.includes(id) ? picked.filter((g) => g !== id) : [...picked, id])
  return (
    <fieldset className={`${css.stack} ${x.fieldset}`}>
      <legend className={css.faint}>仅限用户组（不选 = 不限定）</legend>
      {!readable ? (
        <span className={css.faint}>没有读取用户组的权限（iam.user.read），不能修改名单。{current.length ? `当前限定：${current.map((g) => g.name).join('、')}。` : '当前不限定。'}</span>
      ) : groups.isPending ? (
        <span className={css.faint}>正在读取用户组…</span>
      ) : groups.isError ? (
        <span className={css.faint}>用户组读取失败，稍后再试。</span>
      ) : groups.data.length === 0 ? (
        <span className={css.faint}>还没有用户组，先到「用户 · 用户组」建一个。</span>
      ) : (
        <div className={x.groupChecks}>
          {groups.data.map((g) => (
            <Checkbox key={g.id} label={g.name} checked={picked.includes(g.id)} disabled={disabled} onChange={() => toggle(g.id)} />
          ))}
        </div>
      )}
      <span className={css.faint}>限定后只有名单里的用户组能用这个节点池；未分组（默认）的用户用不了任何限定了用户组的节点池。</span>
      {error && (
        <span className={x.fieldError} role="alert">
          {error}
        </span>
      )}
    </fieldset>
  )
}
