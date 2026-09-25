/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./hookActions 的 useSaveHook，依赖 ./HookDialogs 的 HookModal / SecretModal / EventPicker，依赖 ./logic 的钩子函数，依赖 ./queries，依赖 ./schemas，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 HooksTab（通知与插件 · Webhook 钩子标签）
 * [POS]: admin/screens/system 的 Webhook 钩子（设计稿 t_hooks）：顶部「端点 URL + 订阅事件 + 新建钩子」（code 前端生成且避开现有 code——按 code upsert，撞上会静默覆盖；名称默认取主机名），卡片列表（状态点：停用灰、近 7 天有失败黄；URL；事件 · 近 7 天成功率 · 排队数），卡片上补了启用开关、编辑（契约待补·前端），投递记录展开（状态码、事件 · 第 n 次、耗时 R45、时间）、测试投递（toast「200 · 88 ms」）、删除（先确认）。
 *        写操作都要 platform.plugin.write + reauth；保存（新建 / 编辑 / 启停）按 code upsert 全字段并带幂等键 plugin_hook_save，删除与测试无幂等。事件名只用后端目录（D-A-6 已决（5.A.2），不提供 ticket.replied / node.offline / node.online）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Switch, useToast } from '../../../ui'
import { useSaveHook } from './hookActions'
import { EventPicker, HookModal, SecretModal } from './HookDialogs'
import { deliveryRow, durationLabel, eventsLabel, hookBody, hookFormFrom, hookTone, hostOf, newHookCode, successRate, validateHook } from './logic'
import { useCan, useDeliveries, useFailure, useHooks, useInvalidateSystem } from './queries'
import { hookDeleted, hookTested, type Hook, type HookEvent } from './schemas'
import css from './system.module.css'

export function HooksTab() {
  const hooks = useHooks()
  const can = useCan()
  const [secret, setSecret] = useState<{ secret: string; hint: string } | null>(null)
  const writable = can('platform.plugin.write')

  return (
    <QueryView query={hooks} rows={4} empty={null}>
      {({ hooks: list, events }) => (
        <div className={css.stack}>
          {writable && <CreateBar events={events} existing={list.map((h) => h.code)} onSecret={setSecret} />}
          {list.length === 0 && <Empty title="还没有 Webhook 钩子" description={writable ? '填上端点地址、选好事件，新建一个钩子；事件发生时面板会带签名 POST 过去。' : '有插件管理权限的同事可以在这里新建钩子。'} />}
          {list.map((h) => (
            <HookCard key={h.code} hook={h} events={events} writable={writable} onSecret={setSecret} />
          ))}
          <SecretModal secret={secret} onClose={() => setSecret(null)} />
        </div>
      )}
    </QueryView>
  )
}

function CreateBar({ events, existing, onSecret }: { events: HookEvent[]; existing: string[]; onSecret: (s: { secret: string; hint: string }) => void }) {
  const toast = useToast()
  const [url, setUrl] = useState('')
  const [picked, setPicked] = useState<string[]>(['order.paid', 'order.cancelled'].filter((e) => events.some((x) => x.name === e)))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const save = useSaveHook((r) => {
    toast('钩子已创建')
    setUrl('')
    if (r.secret) onSecret({ secret: r.secret, hint: r.secret_hint ?? '签名密钥只显示这一次，请立刻填进插件那边的配置' })
  }, setErrors)

  const submit = () => {
    const code = newHookCode(existing)
    const form = { name: hostOf(url) || code, description: '', endpoint: url, events: picked, timeoutMs: '5000', maxAttempts: '5', enabled: true, secret: '' }
    const errs = validateHook(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(hookBody(code, form))
  }

  return (
    <section className={css.createBar} aria-label="新建钩子">
      <Input label="端点 URL" fieldClassName={css.createUrl} mono value={url} placeholder="https://" error={errors.endpoint_url} disabled={save.isPending} onChange={(e) => setUrl(e.target.value)} />
      <div className={css.createEvents}>
        <div className={css.label}>订阅事件</div>
        <EventPicker events={events} value={picked} onChange={setPicked} />
        {errors.events && (
          <div className={css.error} role="alert">
            {errors.events}
          </div>
        )}
      </div>
      <Button variant="primary" busy={save.isPending} onClick={submit}>
        新建钩子
      </Button>
    </section>
  )
}

function HookCard({ hook: h, events, writable, onSecret }: { hook: Hook; events: HookEvent[]; writable: boolean; onSecret: (s: { secret: string; hint: string }) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const toggle = useSaveHook((_, body) => toast(body.enabled ? '钩子已启用' : '钩子已停用'))

  const test = useMutation({
    mutationFn: () => api.post(`v1/plugin-hooks/${encodeURIComponent(h.code)}/test`, hookTested, {}),
    onSuccess: (r) => toast(`测试事件已投递 · ${r.response_code} · ${durationLabel(r.duration_ms)}`),
    onError: (e) => fail(e),
  })
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

  const tone = hookTone(h)
  return (
    <section className={css.hook} aria-label={`钩子 ${h.name}`}>
      <div className={css.hookHead}>
        <span className={`${css.dot} ${css[tone]}`} aria-label={tone === 'muted' ? '已停用' : tone === 'warn' ? '近 7 天有失败' : '正常'} />
        <div className={css.hookMain}>
          <div className={css.hookUrl} title={h.endpoint_url}>
            {h.endpoint_url}
          </div>
          <div className={css.faint}>
            {h.name} · {eventsLabel(h.events, events.length)} · 成功率 {successRate(h)}
            {h.queued_count > 0 && ` · 排队 ${h.queued_count}`}
            {h.last_sent_at && ` · 最近送达 ${h.last_sent_at.slice(5)}`}
          </div>
        </div>
        {writable && <Switch aria-label={`启用钩子 ${h.name}`} checked={h.enabled} disabled={toggle.isPending} onChange={(e) => flip(e.target.checked)} />}
        <Button size="xs" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
          {open ? '收起记录' : '投递记录'}
        </Button>
        {writable && (
          <>
            <Button size="xs" busy={test.isPending} onClick={() => test.mutate()}>
              测试投递
            </Button>
            <Button size="xs" onClick={() => setEditing(true)}>
              编辑
            </Button>
            <Button size="xs" variant="ghost" className={css.dangerText} disabled={remove.isPending} onClick={() => setRemoving(true)}>
              删除
            </Button>
          </>
        )}
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
        body={
          <>
            <span className={css.mono}>{h.endpoint_url}</span>
            <br />
            删除后不再向这个地址投递事件，排队中的投递也不会再发出，不能恢复。
          </>
        }
        confirmLabel="删除钩子"
        tone="danger"
        onConfirm={() => {
          remove.mutate()
          setRemoving(false)
        }}
        onCancel={() => setRemoving(false)}
      />
    </section>
  )
}

function Deliveries({ code }: { code: string }) {
  const deliveries = useDeliveries(code)
  return (
    <div className={css.deliveries}>
      <QueryView query={deliveries} rows={3} empty={<div className={css.pad}><span className={css.faint}>还没有投递记录。事件发生后这里会列出最近 50 条。</span></div>}>
        {(rows) =>
          rows.map((d, i) => {
            const r = deliveryRow(d)
            return (
              <div key={`${d.created_at}-${i}`} className={css.delivery} title={d.error_message || undefined}>
                <span className={css[r.tone]}>{r.code}</span>
                <span className={css.ellipsis}>{r.event}</span>
                <span className={css.right}>{r.duration}</span>
                <span className={`${css.right} ${css.faint}`}>{r.at}</span>
              </div>
            )
          })
        }
      </QueryView>
    </div>
  )
}
