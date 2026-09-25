/**
 * [INPUT]: 依赖 react 的 useState / KeyboardEvent，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui 的 Button / Switch / TextArea / useToast，依赖 ./actions、./api、./model 的 MESSAGE_MAX、./MacroManager，依赖 ./Tickets.module.css
 * [OUTPUT]: 对外提供 Composer
 * [POS]: 工单详情底部的回复框：快捷回复标签（点一下填入、不自动发送）与「管理」、内部备注开关（待补·前端，开启时按钮改「添加备注」、不改状态）、⌘↵ 发送、「回复并解决」（先 reply 再 status=resolved，两个请求两把幂等键，失败重试时已成功的那步不再重发）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type KeyboardEvent } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Switch, TextArea, useToast } from '../../../ui'
import { useFailure, useIntentKey } from './actions'
import { okSchema, useInvalidateTickets, useMacros, type TicketDetail } from './api'
import { MacroManager } from './MacroManager'
import { MESSAGE_MAX } from './model'
import css from './Tickets.module.css'

type Mode = 'reply' | 'resolve'

export function Composer({ ticket }: { ticket: TicketDetail }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const macros = useMacros(true)
  const replyKey = useIntentKey()
  const resolveKey = useIntentKey()
  const [body, setBody] = useState('')
  const [note, setNote] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState<Mode | null>(null)
  // 「回复并解决」第一步已成功、第二步失败时记下来：重试只补第二步，不重复回复
  const [replied, setReplied] = useState<string | null>(null)
  const [managing, setManaging] = useState(false)

  const text = body.trim()
  const length = [...text].length
  const tooLong = length > MESSAGE_MAX

  const submit = async (mode: Mode) => {
    if (busy) return
    if (length === 0) return setError(note ? '备注内容不能为空' : '回复内容不能为空')
    if (tooLong) return setError(`最多 ${MESSAGE_MAX} 字`)
    const reply = { body: text, internal_note: note }
    const intent = [ticket.id, reply]
    const fingerprint = JSON.stringify(intent)
    // state 在这次提交里是旧值，用局部变量记「这次意图的回复是否已送达」
    let delivered = replied === fingerprint
    setBusy(mode)
    setError(null)
    try {
      if (!delivered) {
        await api.post(`v1/tickets/${encodeURIComponent(ticket.id)}/reply`, okSchema, { body: reply, idempotencyKey: replyKey.keyFor(intent) })
        delivered = true
        setReplied(fingerprint)
      }
      if (mode === 'resolve') {
        await api.post(`v1/tickets/${encodeURIComponent(ticket.id)}/status`, okSchema, {
          body: { status: 'resolved' },
          idempotencyKey: resolveKey.keyFor(intent),
        })
      }
      replyKey.reset()
      resolveKey.reset()
      setReplied(null)
      setBody('')
      toast(note ? '已添加内部备注' : mode === 'resolve' ? '已回复并解决' : '已回复，状态改为等待用户')
    } catch (e) {
      if (isApiError(e, 'reauth_required')) return
      // 422 的 body 键标在输入框；其它错误 Toast。第一步已成功时说清楚只差解决
      const handled = fail(e, (f) => setError(f.body ?? Object.values(f)[0] ?? '提交失败'))
      if (!handled && delivered && mode === 'resolve') setError('回复已发出，但标记解决失败，可以再点一次「回复并解决」')
    } finally {
      setBusy(null)
      void invalidate()
    }
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
      e.preventDefault()
      void submit('reply')
    }
  }

  return (
    <div className={css.composer}>
      <div className={css.macros}>
        {macros.data?.map((m) => (
          <button
            key={m.id}
            type="button"
            className={css.macro}
            title={m.body}
            onClick={() => {
              setBody(m.body)
              setError(null)
            }}
          >
            {m.title}
          </button>
        ))}
        {macros.isError && <span className={css.macroHint}>快捷回复读取失败</span>}
        <button type="button" className={css.macroManage} onClick={() => setManaging(true)}>
          {macros.data?.length === 0 ? '添加快捷回复' : '管理'}
        </button>
      </div>
      <TextArea
        aria-label={note ? '内部备注' : '回复内容'}
        rows={3}
        value={body}
        onChange={(e) => {
          setBody(e.target.value)
          setError(null)
        }}
        onKeyDown={onKeyDown}
        placeholder={note ? '写给其他客服看的备注，用户看不到，⌘↵ 添加' : '回复用户，⌘↵ 发送'}
        className={note ? css.noteInput : undefined}
        error={error ?? (tooLong ? `最多 ${MESSAGE_MAX} 字（现在 ${length}）` : undefined)}
      />
      <div className={css.composerBar}>
        <Switch label="内部备注" checked={note} onChange={(e) => setNote(e.target.checked)} />
        <div className={css.spacer} />
        {!note && (
          <Button size="md" busy={busy === 'resolve'} disabled={busy !== null} onClick={() => void submit('resolve')}>
            回复并解决
          </Button>
        )}
        <Button size="md" variant="primary" busy={busy === 'reply'} disabled={busy !== null} onClick={() => void submit('reply')}>
          {note ? '添加备注' : '发送回复'}
        </Button>
      </div>
      <MacroManager open={managing} onClose={() => setManaging(false)} macros={macros.data ?? []} />
    </div>
  )
}
