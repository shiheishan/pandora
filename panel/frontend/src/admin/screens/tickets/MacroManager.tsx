/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / ConfirmModal / Empty / Input / Modal / TextArea / useToast，依赖 ../../actions 的 useFailure，依赖 ./api 的 Macro / macroSavedSchema / okSchema / useInvalidateTickets，依赖 ./Tickets.module.css
 * [OUTPUT]: 对外提供 MacroManager
 * [POS]: 快捷回复「管理」对话框（待补·前端，设计稿没有管理入口）：列表、新建、编辑、删除，对应 v1/ticket-macros 四个接口（R42，配置类写操作不带幂等、不要 reauth）；标题 1–20 字、正文 1–5000 字，服务端 fields.title / body 标到对应输入框
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, TextArea, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { macroSavedSchema, okSchema, useInvalidateTickets, type Macro } from './api'
import css from './Tickets.module.css'

const TITLE_MAX = 20
const BODY_MAX = 5000

interface Draft {
  id: string | null
  title: string
  body: string
  sort_order: number
}

export function MacroManager({ open, onClose, macros }: { open: boolean; onClose: () => void; macros: readonly Macro[] }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const [draft, setDraft] = useState<Draft | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [saving, setSaving] = useState(false)
  const [removing, setRemoving] = useState<Macro | null>(null)

  const edit = (d: Draft | null) => {
    setDraft(d)
    setErrors({})
  }
  const close = () => {
    edit(null)
    onClose()
  }

  const save = async () => {
    if (!draft) return
    const title = draft.title.trim()
    const body = draft.body.trim()
    const local: Record<string, string> = {}
    if ([...title].length < 1 || [...title].length > TITLE_MAX) local.title = `标题 1 到 ${TITLE_MAX} 个字`
    if ([...body].length < 1 || [...body].length > BODY_MAX) local.body = `内容 1 到 ${BODY_MAX} 个字`
    if (Object.keys(local).length) return setErrors(local)
    setSaving(true)
    try {
      const path = draft.id ? `v1/ticket-macros/${encodeURIComponent(draft.id)}` : 'v1/ticket-macros'
      await api.post(path, macroSavedSchema, { body: { title, body, sort_order: draft.sort_order } })
      toast(draft.id ? '快捷回复已保存' : '已新建快捷回复')
      edit(null)
      void invalidate('macros')
    } catch (e) {
      fail(e, setErrors)
    } finally {
      setSaving(false)
    }
  }

  const nextOrder = macros.reduce((max, m) => Math.max(max, m.sort_order), 0) + 10

  return (
    <>
      <Modal
        open={open && removing === null}
        onClose={close}
        dismissible={!saving}
        size="md"
        title={draft ? (draft.id ? '编辑快捷回复' : '新建快捷回复') : '快捷回复'}
        actions={
          draft ? (
            <>
              <Button size="dialog" onClick={() => edit(null)} disabled={saving}>
                返回列表
              </Button>
              <Button size="dialog" variant="primary" busy={saving} onClick={() => void save()}>
                保存
              </Button>
            </>
          ) : (
            <>
              <Button size="dialog" onClick={close}>
                完成
              </Button>
              <Button size="dialog" variant="primary" onClick={() => edit({ id: null, title: '', body: '', sort_order: nextOrder })}>
                新建
              </Button>
            </>
          )
        }
      >
        {draft ? (
          <div className={css.macroForm}>
            <Input
              label="标题"
              hint={`显示在回复框上方，最多 ${TITLE_MAX} 字`}
              value={draft.title}
              onChange={(e) => setDraft({ ...draft, title: e.target.value })}
              error={errors.title}
              data-autofocus=""
            />
            <TextArea label="内容" rows={6} value={draft.body} onChange={(e) => setDraft({ ...draft, body: e.target.value })} error={errors.body} hint="点标签后填进回复框，发送前还能改。" />
            <Input
              label="排序"
              type="number"
              hint="数字小的排前面"
              value={String(draft.sort_order)}
              onChange={(e) => setDraft({ ...draft, sort_order: Number.parseInt(e.target.value, 10) || 0 })}
              error={errors.sort_order}
            />
          </div>
        ) : macros.length === 0 ? (
          <Empty bare title="还没有快捷回复" description="把常用的答复存下来，回复时点一下就能填进去。" />
        ) : (
          <ul className={css.macroList}>
            {macros.map((m) => (
              <li key={m.id} className={css.macroItem}>
                <div className={css.macroText}>
                  <div className={css.macroTitle}>{m.title}</div>
                  <div className={css.macroBody}>{m.body}</div>
                </div>
                <Button size="xs" variant="ghost" onClick={() => edit({ id: m.id, title: m.title, body: m.body, sort_order: m.sort_order })}>
                  编辑
                </Button>
                <Button size="xs" variant="ghost" onClick={() => setRemoving(m)}>
                  删除
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Modal>
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
