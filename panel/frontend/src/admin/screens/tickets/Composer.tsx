import { useId, useRef, useState, type KeyboardEvent } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Switch, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { okSchema, useInvalidateTickets, useMacros, type Macro, type TicketDetail } from './api'
import css from './Composer.module.css'
import { MacroManager } from './MacroManager'
import { MESSAGE_MAX } from './model'

type Mode = 'reply' | 'resolve'

/** 快捷回复条最多露几个 chip，其余进「全部 N」浮层（10-16 设计稿） */
const MACRO_CHIPS = 4

export function Composer({ ticket }: { ticket: TicketDetail }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateTickets()
  const macros = useMacros()
  const replyKey = useIntentKey()
  const resolveKey = useIntentKey()
  const errorId = useId()
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
  const displayError = error ?? (tooLong ? `最多 ${MESSAGE_MAX} 字（现在 ${length}）` : null)

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
      // 422 的 body 键标在输入框；其它错误 Toast。第一步已成功时说清楚只差解决。
      // 失败的是哪一步就交哪一把键：回复已送达时失败的只可能是标记解决
      const handled = fail(e, { fields: (f) => setError(f.body ?? Object.values(f)[0] ?? '提交失败'), intent: delivered ? resolveKey : replyKey })
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

  const applyMacro = (m: Macro) => {
    setBody(m.body)
    setError(null)
  }

  const all = macros.data ?? []
  const chips = all.slice(0, MACRO_CHIPS)
  const hasMore = all.length > MACRO_CHIPS

  return (
    <div className={note ? `${css.composer} ${css.note}` : css.composer}>
      <div className={css.macros}>
        {macros.isError ? (
          <span className={css.macroHint}>快捷回复读取失败</span>
        ) : (
          <span className={css.macroLabel}>快捷回复</span>
        )}
        {!macros.isError &&
          chips.map((m) => (
            <button key={m.id} type="button" className={css.macro} title={m.body} onClick={() => applyMacro(m)}>
              {m.title}
            </button>
          ))}
        {!macros.isError && hasMore && <MacroAllButton macros={all} onUse={applyMacro} />}
        <span className={css.spacer} aria-hidden="true" />
        <button type="button" className={css.macroManage} onClick={() => setManaging(true)}>
          {all.length === 0 ? '添加快捷回复' : '管理'}
        </button>
      </div>
      <textarea
        className={css.textarea}
        aria-label={note ? '内部备注' : '回复内容'}
        aria-invalid={displayError ? true : undefined}
        aria-describedby={displayError ? errorId : undefined}
        rows={3}
        value={body}
        onChange={(e) => {
          setBody(e.target.value)
          setError(null)
        }}
        onKeyDown={onKeyDown}
        placeholder={note ? '写给其他客服看的备注，用户看不到，⌘↵ 添加' : '回复用户，⌘↵ 发送'}
      />
      {displayError && (
        <p id={errorId} role="alert" className={css.error}>
          {displayError}
        </p>
      )}
      <div className={css.bar}>
        <Switch label="内部备注" checked={note} onChange={(e) => setNote(e.target.checked)} />
        <span className={css.spacer} aria-hidden="true" />
        {!note && (
          <Button size="sm" busy={busy === 'resolve'} disabled={busy !== null} onClick={() => void submit('resolve')}>
            回复并解决
          </Button>
        )}
        <Button size="sm" variant="primary" busy={busy === 'reply'} disabled={busy !== null} onClick={() => void submit('reply')}>
          {note ? '添加备注' : '发送回复'}
        </Button>
      </div>
      <MacroManager open={managing} onClose={() => setManaging(false)} macros={all} />
    </div>
  )
}

/** 「全部 N」按钮 + 向上弹出的搜索浮层：Esc 与点外面关闭，Esc 不冒泡关掉外层的弹窗 */
function MacroAllButton({ macros, onUse }: { macros: readonly Macro[]; onUse: (m: Macro) => void }) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)

  const toggle = () => {
    if (open) return setOpen(false)
    setQuery('')
    setOpen(true)
    // 浮层渲染后再聚焦搜索框
    setTimeout(() => searchRef.current?.focus(), 0)
  }

  // Esc 关闭时拦住，别把外层的 MacroManager 弹窗一起关了
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      setOpen(false)
    }
  }

  const text = query.trim().toLowerCase()
  const matched = macros.filter((m) => !text || `${m.title}${m.body}`.toLowerCase().includes(text))

  return (
    <div className={css.macroAllWrap} onKeyDown={onKeyDown}>
      <button type="button" className={open ? `${css.macroAll} ${css.macroAllOpen}` : css.macroAll} aria-expanded={open} onClick={toggle}>
        全部 {macros.length}
        <svg className={css.macroAllCaret} viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="m18 15-6-6-6 6" />
        </svg>
      </button>
      {open && (
        <>
          {/* 透明遮罩：点浮层外面就收起（mousedown 在遮罩上，不会进浮层） */}
          <div className={css.macroAllScrim} onMouseDown={() => setOpen(false)} aria-hidden="true" />
          <div role="listbox" aria-label="全部快捷回复" className={css.macroAllPanel}>
            <input
              ref={searchRef}
              className={css.macroAllSearch}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索快捷回复"
              aria-label="搜索快捷回复"
            />
            <div className={css.macroAllList}>
              {matched.map((m) => (
                <button
                  key={m.id}
                  type="button"
                  className={css.macroAllItem}
                  onClick={() => {
                    onUse(m)
                    setOpen(false)
                  }}
                >
                  <span className={css.macroAllTitle}>{m.title}</span>
                  <span className={css.macroAllBody}>{m.body}</span>
                </button>
              ))}
              {matched.length === 0 && <div className={css.macroAllEmpty}>没有匹配的快捷回复</div>}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
