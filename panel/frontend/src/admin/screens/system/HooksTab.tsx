import { Add01Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, QueryView, Switch, useToast } from '../../../ui'
import { PageActions, Row, RowList, StatusDot } from '../../kit'
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
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [creating, setCreating] = useState(false)
  const [secret, setSecret] = useState<{ secret: string; hint: string } | null>(null)
  // 编辑与删除确认框放在列表外：RowList 里只放 listitem（行与展开的投递记录）
  const [editing, setEditing] = useState<Hook | null>(null)
  const [removing, setRemoving] = useState<Hook | null>(null)

  const remove = useMutation({
    mutationFn: (code: string) => api.delete(`v1/plugin-hooks/${encodeURIComponent(code)}`, hookDeleted),
    onSuccess: () => {
      toast('钩子已删除')
      void invalidate('hooks')
    },
    onError: (e) => fail(e),
  })

  return (
    <>
      <PageActions>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <HugeiconsIcon icon={Add01Icon} size={16} aria-hidden="true" />
          新建钩子
        </Button>
      </PageActions>
      <QueryView query={hooks} rows={4} empty={null}>
        {({ hooks: list, events }) => (
          <div className={shared.stack}>
            <div className={css.tsum}>
              {list.length} 个钩子 · 启用 {list.filter((h) => h.enabled).length}
            </div>
            {list.length === 0 ? (
              <Empty bare title="还没有钩子" description="点右上角「新建钩子」填上端点 URL、选好事件就能创建。" />
            ) : (
              <Card flush>
                <RowList label="Webhook 钩子">
                  {list.map((h) => (
                    <HookRowItem key={h.code} hook={h} events={events} removing={remove.isPending} onEdit={() => setEditing(h)} onRemove={() => setRemoving(h)} />
                  ))}
                </RowList>
              </Card>
            )}
            {editing && (
              <HookModal
                hook={editing}
                events={events}
                onClose={() => setEditing(null)}
                onSaved={(r) => {
                  setEditing(null)
                  if (r.secret) setSecret({ secret: r.secret, hint: r.secret_hint ?? '' })
                }}
              />
            )}
            <CreateHookModal open={creating} events={events} existing={list.map((h) => h.code)} onClose={() => setCreating(false)} onCreated={setSecret} />
            <SecretModal secret={secret} onClose={() => setSecret(null)} />
          </div>
        )}
      </QueryView>
      <ConfirmModal
        open={removing !== null}
        title="删除这个钩子？"
        body="之后的事件不会再投递到这个端点，投递记录一起删除。"
        confirmLabel="删除钩子"
        tone="danger"
        onConfirm={() => {
          if (removing) remove.mutate(removing.code)
          setRemoving(null)
        }}
        onCancel={() => setRemoving(null)}
      />
    </>
  )
}

/** 一个钩子：Row 本身是 listitem；展开时紧跟一个 listitem 的投递记录块 */
function HookRowItem({ hook: h, events, removing, onEdit, onRemove }: { hook: Hook; events: HookEvent[]; removing: boolean; onEdit: () => void; onRemove: () => void }) {
  const toast = useToast()
  const [open, setOpen] = useState(false)
  const toggle = useSaveHook((_, body) => toast(body.enabled ? '钩子已启用' : '钩子已停用'))

  const flip = (on: boolean) => {
    if (on && h.events.length === 0) return toast('启用前至少要订阅一个事件，先在「编辑」里选事件', 'danger')
    toggle.mutate(hookBody(h.code, { ...hookFormFrom(h), enabled: on }))
  }

  const tone = hookTone(h)
  const statusTone = tone === 'ok' ? 'ok' : tone === 'warn' ? 'warn' : 'off'
  const healthText = hookHealth(h)
  const healthToneClass = tone === 'ok' ? css.healthOk : tone === 'warn' ? css.healthWarn : css.healthMuted

  return (
    <>
      <Row
        align="start"
        lead={
          <StatusDot tone={statusTone}>
            <span className={css.srOnly}>{healthText}</span>
          </StatusDot>
        }
        title={
          <span>
            {h.name} <span className={`${css.health} ${healthToneClass}`}>{healthText}</span>
          </span>
        }
        meta={
          <span className={css.url} title={h.endpoint_url}>
            {h.endpoint_url}
          </span>
        }
        extra={<span className={css.events}>{eventsLabel(h.events, events)}</span>}
        actions={
          <span className={css.actions}>
            <Switch aria-label={`启用钩子 ${h.name}`} checked={h.enabled} disabled={toggle.isPending} onChange={(e) => flip(e.target.checked)} />
            <button type="button" className={css.linkBtn} aria-expanded={open} onClick={() => setOpen((v) => !v)}>
              {open ? '收起记录' : '投递记录'}
            </button>
            <button type="button" className={`${css.linkBtn} ${css.hideM}`} onClick={onEdit}>
              编辑
            </button>
            <button type="button" className={`${css.linkDanger} ${css.hideM}`} disabled={removing} onClick={onRemove}>
              删除
            </button>
          </span>
        }
      />
      {open && (
        <div role="listitem" aria-label={`${h.name} 的投递记录`} className={css.dl}>
          <Deliveries code={h.code} />
        </div>
      )}
    </>
  )
}

function Deliveries({ code }: { code: string }) {
  const deliveries = useDeliveries(code)
  return (
    <QueryView query={deliveries} rows={3} empty={<div className={css.delivEmpty}>还没有投递记录。事件发生后这里会列出最近 50 条。</div>}>
      {(rows) => (
        <table className={css.delivTable}>
          <thead>
            <tr>
              <th>时间</th>
              <th>事件</th>
              <th className={css.num}>响应</th>
              <th className={css.num}>耗时</th>
              <th className={css.num}>次数</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((d, i) => {
              const r = deliveryRow(d)
              return (
                <tr key={`${d.created_at}-${i}`} title={d.error_message || undefined}>
                  <td className={`${css.at} ${css.dim}`}>{r.at}</td>
                  <td className={css.eventMono}>{r.event}</td>
                  <td className={`${css.num} ${css.code} ${TONE_CLASS[r.tone] ?? ''}`}>{r.code}</td>
                  <td className={`${css.at} ${css.num} ${css.dim}`}>{r.duration}</td>
                  <td className={`${css.at} ${css.num} ${css.dim}`}>{r.attempts}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </QueryView>
  )
}
