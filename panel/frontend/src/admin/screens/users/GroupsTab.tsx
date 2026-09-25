/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatCount，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Card / ConfirmModal / Empty / Input / QueryView / Table / useToast，依赖 ../../actions 的 useCan / useFailure，依赖 ./api 的 useUserGroups / useInvalidateUsers / groupSavedSchema / okSchema / UserGroup，依赖 ./model 的 groupBlocker / groupRefs / exclusivePoolsLabel / EXCLUSIVE_NONE_HINT，依赖 ./Users.module.css 与 ./Ops.module.css
 * [OUTPUT]: 对外提供 GroupsTab
 * [POS]: 用户页「用户组」标签（#/users/groups）：左表格（名称与说明、成员、被引用、查看成员 / 编辑 / 删除），右「新建用户组」卡片（名称、说明、折叠的「标识（英文）」）。契约后台-03：「可用节点池」列（R104 exclusive_pools，空显示「—」并悬停说明只能用未限定的池）；编辑是行内改名称与说明（code 不可改）；删除在节点池名单、成员或任一引用不为 0 时置灰并说明原因（顺序同后端），后端 409 的原文兜底。写操作要 iam.user.write，只读账号只看表格
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatCount } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Table, useToast, type TableColumn } from '../../../ui'
import { useCan, useFailure } from '../../actions'
import { groupSavedSchema, okSchema, useInvalidateUsers, useUserGroups, type UserGroup } from './api'
import { EXCLUSIVE_NONE_HINT, exclusivePoolsLabel, groupBlocker, groupRefs } from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

interface Draft {
  id: string
  name: string
  description: string
}

export function GroupsTab() {
  const groups = useUserGroups()
  const can = useCan()
  const canWrite = can('iam.user.write')
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const [draft, setDraft] = useState<Draft | null>(null)
  const [draftError, setDraftError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState<UserGroup | null>(null)

  const edit = (g: UserGroup) => {
    setDraft({ id: g.id, name: g.name, description: g.description })
    setDraftError(null)
  }
  const save = async () => {
    if (!draft) return
    if (!draft.name.trim()) return setDraftError('名称必填')
    setSaving(true)
    try {
      await api.post(`v1/user-groups/${encodeURIComponent(draft.id)}`, groupSavedSchema, { body: { name: draft.name.trim(), description: draft.description.trim() } })
      toast('用户组已保存')
      setDraft(null)
      void invalidate(true)
    } catch (e) {
      if (!fail(e, (f) => setDraftError(f.name ?? Object.values(f)[0] ?? null))) void invalidate(true)
    } finally {
      setSaving(false)
    }
  }
  const remove = async () => {
    if (!deleting) return
    try {
      await api.delete(`v1/user-groups/${encodeURIComponent(deleting.id)}`, okSchema)
      toast(`已删除用户组「${deleting.name}」`)
    } catch (e) {
      // 409 的原文说清了是成员、套餐、价格还是优惠券挡住了
      fail(e)
    }
    setDeleting(null)
    void invalidate(true)
  }

  const columns: TableColumn<UserGroup>[] = [
    {
      key: 'name',
      header: '用户组',
      render: (g) =>
        draft?.id === g.id ? (
          <span className={ops.groupEdit}>
            <Input size="sm" aria-label="名称" value={draft.name} error={draftError ?? undefined} onChange={(e) => setDraft({ ...draft, name: e.target.value })} data-autofocus="" />
            <Input size="sm" aria-label="说明" value={draft.description} placeholder="说明（选填）" onChange={(e) => setDraft({ ...draft, description: e.target.value })} />
          </span>
        ) : (
          <span className={css.stack}>
            <span className={ops.groupName}>{g.name}</span>
            <span className={css.small}>
              {g.description || '—'} · <span className={css.mono}>{g.code}</span>
            </span>
          </span>
        ),
    },
    { key: 'users', header: '成员', width: '72px', align: 'right', mono: true, render: (g) => formatCount(g.users) },
    {
      key: 'pools',
      header: '可用节点池',
      width: '130px',
      render: (g) => (
        <span className={css.muted} title={g.exclusive_pools.length ? undefined : EXCLUSIVE_NONE_HINT}>
          {exclusivePoolsLabel(g)}
        </span>
      ),
    },
    { key: 'refs', header: '被引用', width: '150px', render: (g) => <span className={css.muted}>{groupRefs(g)}</span> },
    {
      key: 'ops',
      header: '',
      width: canWrite ? '190px' : '90px',
      align: 'right',
      render: (g) => {
        if (draft?.id === g.id)
          return (
            <span className={ops.rowActions}>
              <Button size="xs" variant="ghost" disabled={saving} onClick={() => setDraft(null)}>
                取消
              </Button>
              <Button size="xs" variant="primary" busy={saving} onClick={() => void save()}>
                保存
              </Button>
            </span>
          )
        const blocker = groupBlocker(g)
        return (
          <span className={ops.rowActions}>
            <a className={css.link} href={href('/users/list', { g: g.id })}>
              查看成员
            </a>
            {canWrite && (
              <>
                <Button size="xs" variant="ghost" onClick={() => edit(g)}>
                  编辑
                </Button>
                <Button size="xs" variant="ghost" className={blocker ? undefined : css.dangerText} disabled={blocker !== null} title={blocker ?? undefined} onClick={() => setDeleting(g)}>
                  删除
                </Button>
              </>
            )}
          </span>
        )
      },
    },
  ]

  return (
    <div className={canWrite ? ops.split : undefined}>
      <div className={ops.plainTable}>
        <QueryView
          query={groups}
          rows={4}
          isEmpty={(d) => d.length === 0}
          empty={<Empty bare title="还没有用户组" description={canWrite ? '新注册用户默认不属于任何组；在右侧建一个，再到用户详情里分配。' : '新注册用户默认不属于任何组。'} />}
        >
          {(d) => <Table label="用户组" columns={columns} rows={d} rowKey={(g) => g.id} />}
        </QueryView>
      </div>
      {canWrite && <CreateGroup onDone={() => void invalidate(true)} />}
      <ConfirmModal
        open={deleting !== null}
        title={`删除用户组「${deleting?.name ?? ''}」？`}
        body="删除后无法恢复。这个组已经没有成员，也没有套餐、价格或优惠券引用它。"
        confirmLabel="删除"
        tone="danger"
        onConfirm={remove}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 新建：POST v1/user-groups（iam.user.write，无幂等）。不填标识时后端由名称生成，纯中文名得 group-<8 位十六进制>
// ---------------------------------------------------------------------------
function CreateGroup({ onDone }: { onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [code, setCode] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const submit = async () => {
    if (!name.trim()) return setErrors({ name: '请填写名称' })
    setBusy(true)
    try {
      const body: Record<string, string> = { name: name.trim() }
      if (description.trim()) body.description = description.trim()
      if (code.trim()) body.code = code.trim()
      await api.post('v1/user-groups', groupSavedSchema, { body })
      toast(`已创建用户组「${name.trim()}」`)
      setName('')
      setDescription('')
      setCode('')
      setErrors({})
      setAdvanced(false)
      onDone()
    } catch (e) {
      // 409：标识撞了（没填标识时是名称生成的那个），提示到高级选项里换一个
      if (isApiError(e, 'conflict')) {
        setAdvanced(true)
        setErrors({ code: code.trim() ? e.message : `${e.message}：名称生成的标识已被占用，请在这里换一个` })
      } else fail(e, setErrors)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="新建用户组">
      <form
        className={ops.formStack}
        onSubmit={(e) => {
          e.preventDefault()
          void submit()
        }}
      >
        <Input
          label="名称"
          placeholder="例如：企业客户"
          value={name}
          error={errors.name}
          onChange={(e) => {
            setName(e.target.value)
            setErrors({})
          }}
        />
        <Input label="说明" placeholder="选填" value={description} onChange={(e) => setDescription(e.target.value)} />
        <details className={ops.advanced} open={advanced} onToggle={(e) => setAdvanced(e.currentTarget.open)}>
          <summary>高级选项</summary>
          <Input
            label="标识（英文）"
            mono
            placeholder="不填则由名称生成"
            value={code}
            error={errors.code}
            hint="创建后不能修改"
            onChange={(e) => {
              setCode(e.target.value)
              setErrors({})
            }}
          />
        </details>
        <Button type="submit" variant="primary" busy={busy}>
          创建
        </Button>
        <p className={css.small}>在用户详情的「画像」里给单个用户分配用户组。</p>
      </form>
    </Card>
  )
}
