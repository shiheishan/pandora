import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatCount } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Table, Tag, useToast, type TableColumn } from '../../../ui'
import { useFailure } from '../../actions'
import { groupSavedSchema, okSchema, useInvalidateUsers, useUserGroups, type UserGroup } from './api'
import { EXCLUSIVE_NONE_HINT, GROUP_DISABLE_NOTICE, exclusivePoolsLabel, groupBlocker, GROUP_DELETE_NOTICE, groupRefs } from './model'
import ops from './Ops.module.css'
import css from './Users.module.css'

interface Draft {
  id: string
  name: string
  description: string
}

export function GroupsTab() {
  const groups = useUserGroups()
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const client = useQueryClient()
  // 用户组变了：用户列表与分组下拉、套餐页（删组会连带删专属价）、订阅域名页自己的用户组查询都要刷新
  const refresh = () =>
    void Promise.all([
      invalidate(true),
      client.invalidateQueries({ queryKey: ['admin', 'plans'] }),
      client.invalidateQueries({ queryKey: ['admin', 'nodes', 'subscription', 'user-groups'] }),
    ])
  const [draft, setDraft] = useState<Draft | null>(null)
  const [draftError, setDraftError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState<UserGroup | null>(null)
  const [disabling, setDisabling] = useState<UserGroup | null>(null)
  const [statusBusy, setStatusBusy] = useState<string | null>(null)

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
      refresh()
    } catch (e) {
      if (!fail(e, (f) => setDraftError(f.name ?? Object.values(f)[0] ?? null))) refresh()
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
      // 409 的原文说清了是成员、节点池、套餐、优惠券挡住，还是专属价已经有人买过
      fail(e)
    }
    setDeleting(null)
    refresh()
  }
  const setStatus = async (g: UserGroup, disabled: boolean) => {
    setStatusBusy(g.id)
    try {
      await api.post(`v1/user-groups/${encodeURIComponent(g.id)}/status`, okSchema, { body: { disabled } })
      toast(disabled ? `已停用用户组「${g.name}」` : `已启用用户组「${g.name}」`)
    } catch (e) {
      fail(e)
    } finally {
      setStatusBusy(null)
      setDisabling(null)
      refresh()
    }
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
            <span className={ops.groupName}>
              {g.name}
              {g.status === 'disabled' ? <> <Tag tone="neutral">已停用</Tag></> : null}
            </span>
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
      width: '240px',
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
            <>
              <Button size="xs" variant="ghost" onClick={() => edit(g)}>
                编辑
              </Button>
              {g.status === 'disabled' ? (
                <Button size="xs" variant="ghost" busy={statusBusy === g.id} onClick={() => void setStatus(g, false)}>
                  启用
                </Button>
              ) : (
                <Button size="xs" variant="ghost" onClick={() => setDisabling(g)}>
                  停用
                </Button>
              )}
              <Button size="xs" variant="ghost" className={blocker ? undefined : css.dangerText} disabled={blocker !== null} title={blocker ?? undefined} onClick={() => setDeleting(g)}>
                删除
              </Button>
            </>
          </span>
        )
      },
    },
  ]

  return (
    <div className={ops.split}>
      <div className={ops.plainTable}>
        <QueryView
          query={groups}
          rows={4}
          isEmpty={(d) => d.length === 0}
          empty={<Empty bare title="还没有用户组" description="新注册用户默认不属于任何组；在右侧建一个，再到用户详情里分配。" />}
        >
          {(d) => <Table label="用户组" columns={columns} rows={d} rowKey={(g) => g.id} />}
        </QueryView>
      </div>
      <CreateGroup onDone={refresh} />
      <ConfirmModal
        open={disabling !== null}
        title={`停用用户组「${disabling?.name ?? ''}」？`}
        body={GROUP_DISABLE_NOTICE}
        confirmLabel="停用"
        onConfirm={async () => {
          if (disabling) await setStatus(disabling, true)
        }}
        onCancel={() => setDisabling(null)}
      />
      <ConfirmModal
        open={deleting !== null}
        title={`删除用户组「${deleting?.name ?? ''}」？`}
        body={deleting ? GROUP_DELETE_NOTICE : ''}
        confirmLabel="删除"
        tone="danger"
        onConfirm={remove}
        onCancel={() => setDeleting(null)}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 新建：POST v1/user-groups（无幂等）。不填标识时后端由名称生成，纯中文名得 group-<8 位十六进制>
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
