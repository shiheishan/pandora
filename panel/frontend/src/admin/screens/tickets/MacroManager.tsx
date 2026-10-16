import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, TextArea, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { macroSavedSchema, okSchema, useInvalidateTickets, type Macro } from './api'
import css from './Tickets.module.css'

const TITLE_MAX = 20
const BODY_MAX = 5000

export function MacroManager({ open, onClose, macros }: { open: boolean; onClose: () => void; macros: readonly Macro[] }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  // 新增的两个输入框
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [adding, setAdding] = useState(false)
  // 「修改」小弹窗（在列表弹窗之上）
  const [editing, setEditing] = useState<Macro | null>(null)
  const [saving, setSaving] = useState(false)
  const [removing, setRemoving] = useState<Macro | null>(null)

  const closeEditing = () => {
    setEditing(null)
    setErrors({})
  }

  /** 底部「添加」：两个都填了才新建；都空时等于关闭；只填一个标到对应输入 */
  const add = async () => {
    const t = title.trim()
    const b = body.trim()
    if (!t && !b) return onClose()
    const local: Record<string, string> = {}
    if ([...t].length < 1 || [...t].length > TITLE_MAX) local.title = `标题 1 到 ${TITLE_MAX} 个字`
    if ([...b].length < 1 || [...b].length > BODY_MAX) local.body = `内容 1 到 ${BODY_MAX} 个字`
    if (Object.keys(local).length) return setErrors(local)
    setAdding(true)
    try {
      const sort_order = macros.reduce((max, m) => Math.max(max, m.sort_order), 0) + 10
      await api.post('v1/ticket-macros', macroSavedSchema, { body: { title: t, body: b, sort_order } })
      toast('快捷回复已添加')
      setTitle('')
      setBody('')
      setErrors({})
      void invalidate('macros')
    } catch (e) {
      fail(e, setErrors)
    } finally {
      setAdding(false)
    }
  }

  /** 修改小弹窗里的「保存」：sort_order 沿用原值 */
  const save = async () => {
    if (!editing) return
    const t = editing.title.trim()
    const b = editing.body.trim()
    const local: Record<string, string> = {}
    if ([...t].length < 1 || [...t].length > TITLE_MAX) local.title = `标题 1 到 ${TITLE_MAX} 个字`
    if ([...b].length < 1 || [...b].length > BODY_MAX) local.body = `内容 1 到 ${BODY_MAX} 个字`
    if (Object.keys(local).length) return setErrors(local)
    setSaving(true)
    try {
      await api.post(`v1/ticket-macros/${encodeURIComponent(editing.id)}`, macroSavedSchema, { body: { title: t, body: b, sort_order: editing.sort_order } })
      toast(`已保存「${t}」`)
      closeEditing()
      void invalidate('macros')
    } catch (e) {
      fail(e, setErrors)
    } finally {
      setSaving(false)
    }
  }

  return (
    <>
      <Modal
        // 打开「修改」或删除确认时把列表弹窗藏起来，回来时状态还在
        open={open && editing === null && removing === null}
        onClose={onClose}
        dismissible={!adding}
        size={560}
        title="快捷回复"
        actions={
          <>
            <Button size="dialog" onClick={onClose}>
              完成
            </Button>
            <Button size="dialog" variant="primary" busy={adding} onClick={() => void add()}>
              添加
            </Button>
          </>
        }
      >
        {macros.length === 0 ? (
          <Empty bare title="还没有快捷回复" description="把常用的答复存下来，回复时点一下就能填进去。" />
        ) : (
          <ul className={css.macroList}>
            {macros.map((m) => (
              <li key={m.id} className={css.macroItem}>
                <div className={css.macroText}>
                  <div className={css.macroTitle}>{m.title}</div>
                  <div className={css.macroBody}>{m.body}</div>
                </div>
                <Button size="xs" variant="ghost" onClick={() => setEditing(m)}>
                  修改
                </Button>
                <Button size="xs" variant="ghost" className={css.macroActionDanger} onClick={() => setRemoving(m)}>
                  删除
                </Button>
              </li>
            ))}
          </ul>
        )}
        <div className={css.macroForm}>
          <Input
            value={title}
            onChange={(e) => {
              setTitle(e.target.value)
              setErrors({})
            }}
            placeholder="标题，如「线路排查」"
            aria-label="新快捷回复标题"
            error={errors.title}
          />
          <TextArea
            rows={3}
            value={body}
            onChange={(e) => {
              setBody(e.target.value)
              setErrors({})
            }}
            placeholder="回复正文"
            aria-label="新快捷回复正文"
            error={errors.body}
          />
        </div>
      </Modal>
      <EditDialog
        macro={editing}
        errors={errors}
        saving={saving}
        onErrors={setErrors}
        onChange={setEditing}
        onClose={closeEditing}
        onSave={save}
      />
      <ConfirmModal
        open={removing !== null}
        title={`删除「${removing?.title ?? ''}」？`}
        body="所有客服的回复框里都不会再出现这条快捷回复，已经发出的回复不受影响。"
        confirmLabel="删除"
        tone="danger"
        onCancel={() => setRemoving(null)}
        onConfirm={async () => {
          if (!removing) return
          try {
            await api.delete(`v1/ticket-macros/${encodeURIComponent(removing.id)}`, okSchema)
            toast('已删除快捷回复')
            void invalidate('macros')
            setRemoving(null)
          } catch (e) {
            // 已被别人删掉：列表刷新即可
            if (isApiError(e, 'not_found')) {
              void invalidate('macros')
              setRemoving(null)
              return
            }
            fail(e)
          }
        }}
      />
    </>
  )
}

/** 「修改快捷回复」小弹窗（size 480）：打开时列表弹窗隐藏 */
function EditDialog({
  macro,
  errors,
  saving,
  onErrors,
  onChange,
  onClose,
  onSave,
}: {
  macro: Macro | null
  errors: Record<string, string>
  saving: boolean
  onErrors: (errors: Record<string, string>) => void
  onChange: (draft: Macro) => void
  onClose: () => void
  onSave: () => void
}) {
  return (
    <Modal
      open={macro !== null}
      onClose={onClose}
      dismissible={!saving}
      size={480}
      title="修改快捷回复"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={saving} onClick={onSave}>
            保存
          </Button>
        </>
      }
    >
      {macro && (
        <div className={css.macroForm}>
          <Input
            label="标题"
            value={macro.title}
            onChange={(e) => {
              onErrors({})
              onChange({ ...macro, title: e.target.value })
            }}
            error={errors.title}
          />
          <TextArea
            label="回复正文"
            rows={6}
            value={macro.body}
            onChange={(e) => {
              onErrors({})
              onChange({ ...macro, body: e.target.value })
            }}
            error={errors.body}
          />
        </div>
      )}
    </Modal>
  )
}
