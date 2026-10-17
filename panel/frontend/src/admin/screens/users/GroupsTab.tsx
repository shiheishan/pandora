import { PlusSignIcon } from '@hugeicons-pro/core-stroke-rounded'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatCount } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, QueryView, Table, Tag, TextArea, useToast, type TableColumn } from '../../../ui'
import { useFailure } from '../../actions'
import { PageActions } from '../../kit'
import { groupSavedSchema, okSchema, useInvalidateUsers, useUserGroups, type UserGroup } from './api'
import { EXCLUSIVE_NONE_HINT, GROUP_DISABLE_NOTICE, GROUP_DELETE_NOTICE, exclusivePoolsLabel, groupBlocker, groupRefs } from './model'
import gcss from './Groups.module.css'

interface EditDraft {
  id: string
  code: string
  name: string
  description: string
  users: number
  plans: number
  coupons: number
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
  const [editing, setEditing] = useState<EditDraft | null>(null)
  const [editError, setEditError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const [deleting, setDeleting] = useState<UserGroup | null>(null)
  const [disabling, setDisabling] = useState<UserGroup | null>(null)
  const [statusBusy, setStatusBusy] = useState<string | null>(null)

  const openEdit = (g: UserGroup) => {
    setEditing({
      id: g.id,
      code: g.code,
      name: g.name,
      description: g.description,
      users: g.users,
      plans: g.plans,
      coupons: g.coupons,
    })
    setEditError(null)
  }
  const closeEdit = () => {
    setEditing(null)
    setEditError(null)
  }
  const saveEdit = async () => {
    if (!editing) return
    if (!editing.name.trim()) return setEditError('名称必填')
    setSaving(true)
    try {
      await api.post(`v1/user-groups/${encodeURIComponent(editing.id)}`, groupSavedSchema, {
        body: { name: editing.name.trim(), description: editing.description.trim() },
      })
      toast('用户组已保存')
      closeEdit()
      refresh()
    } catch (e) {
      if (!fail(e, (f) => setEditError(f.name ?? Object.values(f)[0] ?? null))) refresh()
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
      render: (g) => (
        <span className={gcss.stack}>
          <span className={gcss.groupName}>
            {g.name}
            {g.status === 'disabled' ? <> <Tag tone="neutral">已停用</Tag></> : null}
          </span>
          {/* 有描述写「描述 · 」，没有只写标识（样稿 td .sub2 + .mono） */}
          <span className={gcss.sub}>
            {g.description ? `${g.description} · ` : ''}
            <span className={gcss.code}>{g.code}</span>
          </span>
        </span>
      ),
    },
    { key: 'users', header: '成员', width: '72px', align: 'right', mono: true, render: (g) => formatCount(g.users) },
    {
      key: 'pools',
      hideOnMobile: true,
      header: '可用节点池',
      width: '140px',
      render: (g) => (
        <span className={gcss.muted} title={g.exclusive_pools.length ? undefined : EXCLUSIVE_NONE_HINT}>
          {exclusivePoolsLabel(g)}
        </span>
      ),
    },
    // groupRefs 空时返回 '—'（model.ts），那时写「未被引用」
    {
      key: 'refs',
      hideOnMobile: true,
      header: '被引用',
      width: '150px',
      render: (g) => <span className={gcss.muted}>{groupRefs(g) === '—' ? '未被引用' : groupRefs(g)}</span>,
    },
    {
      key: 'ops',
      header: '',
      width: '240px',
      align: 'right',
      render: (g) => {
        const blocker = groupBlocker(g)
        return (
          <span className={gcss.rowActions}>
            <a className={gcss.viewLink} href={href('/users/list', { g: g.id })}>
              查看成员
            </a>
            <Button variant="link" size="xs" className={gcss.hideMobile} onClick={() => openEdit(g)}>
              编辑
            </Button>
            {g.status === 'disabled' ? (
              <Button variant="ghost" size="xs" className={`${gcss.dangerHover} ${gcss.hideMobile}`} busy={statusBusy === g.id} onClick={() => void setStatus(g, false)}>
                启用
              </Button>
            ) : (
              <Button variant="ghost" size="xs" className={`${gcss.dangerHover} ${gcss.hideMobile}`} onClick={() => setDisabling(g)}>
                停用
              </Button>
            )}
            <Button
              variant="ghost"
              size="xs"
              className={`${gcss.dangerHover} ${gcss.hideMobile}`}
              disabled={blocker !== null}
              title={blocker ?? undefined}
              onClick={() => setDeleting(g)}
            >
              删除
            </Button>
          </span>
        )
      },
    },
  ]

  return (
    <>
      <PageActions>
        <Button variant="primary" icon={PlusSignIcon} onClick={() => setCreateOpen(true)}>
          新建用户组
        </Button>
      </PageActions>
      <QueryView
        query={groups}
        rows={4}
        isEmpty={(d) => d.length === 0}
        empty={
          <Empty
            bare
            title="还没有用户组"
            description="新注册用户默认不属于任何组；点右上角「新建用户组」建一个，再到用户详情里分配。"
          />
        }
      >
        {(d) => {
          const disabledCount = d.filter((g) => g.status === 'disabled').length
          const memberTotal = d.reduce((a, g) => a + g.users, 0)
          return (
            <Table
              boxed
              label="用户组"
              columns={columns}
              rows={d}
              rowKey={(g) => g.id}
              rowClassName={(g) => (g.status === 'disabled' ? gcss.dimRow : undefined)}
              footer={
                <div className={gcss.foot}>
                  <span>
                    共 {formatCount(d.length)} 个用户组 · {formatCount(disabledCount)} 个已停用 · {formatCount(memberTotal)} 位成员
                  </span>
                  <span className={gcss.footSpacer} />
                  <span className={gcss.footHint}>在用户详情的「资料」里给单个用户分配用户组</span>
                </div>
              }
            />
          )
        }}
      </QueryView>
      <EditGroupModal
        draft={editing}
        error={editError}
        busy={saving}
        onClose={closeEdit}
        onChange={(patch) => {
          if (editing) setEditing({ ...editing, ...patch })
          setEditError(null)
        }}
        onSave={() => void saveEdit()}
      />
      <CreateGroupModal open={createOpen} onClose={() => setCreateOpen(false)} onDone={refresh} />
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
    </>
  )
}

function EditGroupModal({
  draft,
  error,
  busy,
  onClose,
  onChange,
  onSave,
}: {
  draft: EditDraft | null
  error: string | null
  busy: boolean
  onClose: () => void
  onChange: (patch: Partial<Pick<EditDraft, 'name' | 'description'>>) => void
  onSave: () => void
}) {
  const g = draft
  return (
    <Modal
      open={g !== null}
      onClose={onClose}
      title="编辑用户组"
      size={480}
      dismissible={!busy}
      subtitle={
        g ? (
          <>
            标识 <code className={gcss.code}>{g.code}</code> · 创建后不能修改
          </>
        ) : null
      }
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={onSave}>
            保存
          </Button>
        </>
      }
    >
      {g ? (
        <div className={gcss.formStack}>
          <Input
            label="名称"
            value={g.name}
            error={error ?? undefined}
            onChange={(e) => onChange({ name: e.target.value })}
            data-autofocus=""
          />
          <TextArea label="说明" rows={2} placeholder="选填，只在后台可见" value={g.description} onChange={(e) => onChange({ description: e.target.value })} />
          <dl className={gcss.metaDl}>
            <div className={gcss.metaRow}>
              <dt>成员</dt>
              <dd>{formatCount(g.users)} 位</dd>
            </div>
            <div className={gcss.metaRow}>
              <dt>被引用</dt>
              <dd>{groupRefs(g)}</dd>
            </div>
          </dl>
        </div>
      ) : null}
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// 新建：POST v1/user-groups（无幂等）。不填标识时后端由名称生成，纯中文名得 group-<8 位十六进制>
// ---------------------------------------------------------------------------
function CreateGroupModal({ open, onClose, onDone }: { open: boolean; onClose: () => void; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [code, setCode] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const reset = () => {
    setName('')
    setDescription('')
    setCode('')
    setAdvanced(false)
    setErrors({})
  }
  const close = () => {
    if (busy) return
    reset()
    onClose()
  }

  const submit = async () => {
    if (!name.trim()) return setErrors({ name: '请填写名称' })
    setBusy(true)
    try {
      const body: Record<string, string> = { name: name.trim() }
      if (description.trim()) body.description = description.trim()
      if (code.trim()) body.code = code.trim()
      await api.post('v1/user-groups', groupSavedSchema, { body })
      toast(`已创建用户组「${name.trim()}」`)
      reset()
      onClose()
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
    <Modal
      open={open}
      onClose={close}
      title="新建用户组"
      size={480}
      dismissible={!busy}
      actions={
        <>
          <Button size="dialog" onClick={close} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} type="submit" form="create-user-group-form">
            创建
          </Button>
        </>
      }
    >
      <form
        id="create-user-group-form"
        className={gcss.formStack}
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
        <TextArea
          label="说明"
          placeholder="选填"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <details className={gcss.advanced} open={advanced} onToggle={(e) => setAdvanced(e.currentTarget.open)}>
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
      </form>
    </Modal>
  )
}
