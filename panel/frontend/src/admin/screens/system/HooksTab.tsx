import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, QueryView, Switch, useToast } from '../../../ui'
import { useSaveHook } from './hookActions'
import { CreateHookModal, HookModal, SecretModal } from './HookDialogs'
import { deliveryRow, eventsLabel, hookBody, hookFormFrom, hookHealth, hookTone } from './logic'
import { useDeliveries, useFailure, useHooks, useInvalidateSystem } from './queries'
import { hookDeleted, type Hook, type HookEvent } from './schemas'
import css from './hooks.module.css'
import shared from './system.module.css'

const TONE_CLASS: Record<string, string | undefined> = { ok: css.toneOk, warn: css.toneWarn, muted: css.toneMuted }

export function HooksTab() {
  const hooks = useHooks()
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<{ secret: string; hint: string } | null>(null)

  return (
    <QueryView query={hooks} rows={4} empty={null}>
      {({ hooks: list, events }) => (
        <div className={shared.stack}>
          <div className={css.topbar}>
            <span className={css.count}>
              {list.length} 个钩子 · 启用 {list.filter((h) => h.enabled).length}
            </span>
            <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
              新建钩子
            </Button>
          </div>
          {list.length === 0 ? (
            <div className={css.empty}>还没有钩子。点「新建钩子」填上端点 URL、选好事件就能创建。</div>
          ) : (
            <section className={css.list} aria-label="Webhook 钩子">
              {list.map((h) => (
                <HookCard key={h.code} hook={h} events={events} onSecret={setSecret} />
              ))}
            </section>
          )}
          <CreateHookModal open={creating} events={events} existing={list.map((h) => h.code)} onClose={() => setCreating(false)} onCreated={setSecret} />
          <SecretModal secret={secret} onClose={() => setSecret(null)} />
        </div>
      )}
    </QueryView>
  )
}

function HookCard({ hook: h, events, onSecret }: { hook: Hook; events: HookEvent[]; onSecret: (s: { secret: string; hint: string }) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const toggle = useSaveHook((_, body) => toast(body.enabled ? '钩子已启用' : '钩子已停用'))

  const remove = useMutation({
    mutationFn: () => api.delete(`v1/plugin-hooks/${encodeURIComponent(h.code)}`, hookDeleted),
    onSuccess: () => {
      toast('钩子已删除')
      void invalidate('hooks')
    },
    onError: (e) => fail(e),
  })

  const flip = (on: boolean) => {
    if (on && h.events.length === 0) return toast('启用前至少要订阅一个事件，先在「编辑」里选事件', 'danger')
    toggle.mutate(hookBody(h.code, { ...hookFormFrom(h), enabled: on }))
  }

  return (
    <div className={css.row}>
      <div className={css.rowHead}>
        <span className={`${css.dot} ${css[hookTone(h)]}`} role="img" aria-label={hookHealth(h)} />
        <button type="button" className={css.main} onClick={() => setEditing(true)} aria-label={`编辑钩子 ${h.name}`}>
          <span className={css.firstLine}>
            <span className={css.name}>{h.name}</span>
            <span className={css.health}>{hookHealth(h)}</span>
          </span>
          <span className={css.url} title={h.endpoint_url}>
            {h.endpoint_url}
          </span>
          <span className={css.events}>{eventsLabel(h.events, events)}</span>
        </button>
        <div className={css.actions}>
          <Switch aria-label={`启用钩子 ${h.name}`} checked={h.enabled} disabled={toggle.isPending} onChange={(e) => flip(e.target.checked)} />
          <button type="button" className={css.textBtn} aria-expanded={open} onClick={() => setOpen((v) => !v)}>
            {open ? '收起记录' : '投递记录'}
          </button>
          <button type="button" className={css.textBtn} onClick={() => setEditing(true)}>
            编辑
          </button>
          <button type="button" className={`${css.textBtn} ${css.danger}`} disabled={remove.isPending} onClick={() => setRemoving(true)}>
            删除
          </button>
        </div>
      </div>
      {open && <Deliveries code={h.code} />}
      {editing && (
        <HookModal
          hook={h}
          events={events}
          onClose={() => setEditing(false)}
          onSaved={(r) => {
            setEditing(false)
            if (r.secret) onSecret({ secret: r.secret, hint: r.secret_hint ?? '' })
          }}
        />
      )}
      <ConfirmModal
        open={removing}
        title="删除这个钩子？"
        body="之后的事件不会再投递到这个端点，投递记录一起删除。"
        confirmLabel="删除钩子"
        tone="danger"
        onConfirm={() => {
          remove.mutate()
          setRemoving(false)
        }}
        onCancel={() => setRemoving(false)}
      />
    </div>
  )
}

function Deliveries({ code }: { code: string }) {
  const deliveries = useDeliveries(code)
  return (
    <div className={css.detail}>
      <QueryView query={deliveries} rows={3} empty={<div className={css.delivEmpty}>还没有投递记录。事件发生后这里会列出最近 50 条。</div>}>
        {(rows) => (
          <>
            <div className={`${css.delivRow} ${css.delivHead}`} aria-hidden="true">
              <span>时间</span>
              <span>事件</span>
              <span className={css.num}>响应</span>
              <span className={css.num}>耗时</span>
              <span className={css.num}>次数</span>
            </div>
            {rows.map((d, i) => {
              const r = deliveryRow(d)
              return (
                <div key={`${d.created_at}-${i}`} className={css.delivRow} title={d.error_message || undefined}>
                  <span className={css.at}>{r.at}</span>
                  <span>{r.event}</span>
                  <span className={`${css.code} ${css.num} ${TONE_CLASS[r.tone] ?? ''}`}>{r.code}</span>
                  <span className={`${css.at} ${css.num} ${css.dim}`}>{r.duration}</span>
                  <span className={`${css.at} ${css.num} ${css.dim}`}>{r.attempts}</span>
                </div>
              )
            })}
          </>
        )}
      </QueryView>
    </div>
  )
}
