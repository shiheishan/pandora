import { useState } from 'react'
import { Button, Input, Modal, Switch, useToast } from '../../../ui'
import { useSaveHook } from './hookActions'
import { hookBody, hookFormFrom, hostOf, newHookCode, validateHook, type HookForm } from './logic'
import type { Hook, HookEvent } from './schemas'
import css from './hooks.module.css'
import shared from './system.module.css'

/** 新建钩子弹窗（设计稿 nhOpen）：URL + 事件小按钮；创建沿用原 CreateBar 的默认值与 upsert 约定 */
export function CreateHookModal({ open, events, existing, onClose, onCreated }: { open: boolean; events: HookEvent[]; existing: string[]; onClose: () => void; onCreated: (s: { secret: string; hint: string }) => void }) {
  const toast = useToast()
  const defaultPicked = () => ['order.paid'].filter((n) => events.some((e) => e.name === n))
  const [url, setUrl] = useState('')
  const [picked, setPicked] = useState<string[]>(defaultPicked)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const save = useSaveHook((r) => {
    toast('钩子已创建')
    setUrl('')
    setPicked(defaultPicked())
    if (r.secret) onCreated({ secret: r.secret, hint: r.secret_hint ?? '签名密钥只显示这一次，请立刻填进插件那边的配置' })
    onClose()
  }, setErrors)

  const submit = () => {
    const code = newHookCode(existing)
    // 名称默认取主机名；没选事件就先建为停用（启用了没事件的钩子永远不触发，校验也会拦）
    const form: HookForm = { name: hostOf(url) || code, description: '', endpoint: url, events: picked, timeoutMs: '5000', maxAttempts: '5', enabled: picked.length > 0, secret: '' }
    const errs = validateHook(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(hookBody(code, form))
  }

  const busy = save.isPending
  return (
    <Modal
      open={open}
      onClose={onClose}
      size={560}
      dismissible={!busy}
      title="新建钩子"
      subtitle="事件发生时，面板向插件端点投递签名的 JSON"
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={submit}>
            创建
          </Button>
        </>
      }
    >
      <div className={shared.stack}>
        <Input
          label="端点 URL"
          mono
          value={url}
          placeholder="https://"
          error={errors.endpoint_url}
          disabled={busy}
          onChange={(e) => {
            setUrl(e.target.value)
            if (errors.endpoint_url) setErrors((x) => ({ ...x, endpoint_url: '' }))
          }}
        />
        <div>
          <div className={shared.label}>订阅事件</div>
          <div className={css.eventChips}>
            {events.map((e) => {
              const on = picked.includes(e.name)
              return (
                <button key={e.name} type="button" aria-pressed={on} title={e.name} className={on ? `${css.chip} ${css.chipOn}` : css.chip} disabled={busy} onClick={() => setPicked((v) => (on ? v.filter((x) => x !== e.name) : [...v, e.name]))}>
                  {e.desc}
                </button>
              )
            })}
          </div>
          {errors.events && (
            <div className={shared.error} role="alert">
              {errors.events}
            </div>
          )}
        </div>
      </div>
    </Modal>
  )
}

/** 编辑钩子弹窗：基本 / 订阅事件 / 安全 三个小节，保存逻辑不变（全字段 upsert） */
export function HookModal({ hook, events, onClose, onSaved }: { hook: Hook; events: HookEvent[]; onClose: () => void; onSaved: (r: { secret?: string; secret_hint?: string }) => void }) {
  const toast = useToast()
  const [form, setForm] = useState<HookForm>(() => hookFormFrom(hook))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const save = useSaveHook((r) => {
    toast('钩子已保存')
    onSaved(r)
  }, setErrors)
  const edit = (patch: Partial<HookForm>) => setForm((f) => ({ ...f, ...patch }))

  const submit = () => {
    const errs = validateHook(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(hookBody(hook.code, form))
  }

  const busy = save.isPending
  return (
    <Modal
      open
      size={520}
      onClose={onClose}
      title="编辑钩子"
      eyebrow={hook.code}
      actions={
        <>
          <Button size="dialog" variant="ghost" onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={submit}>
            保存
          </Button>
        </>
      }
    >
      <div className={shared.formGrid}>
        <div className={`${shared.span2} ${css.section}`}>基本</div>
        <Input label="名称" value={form.name} error={errors.name} disabled={busy} onChange={(e) => edit({ name: e.target.value })} />
        <Input label="说明（可选）" value={form.description} disabled={busy} onChange={(e) => edit({ description: e.target.value })} />
        <Input label="端点 URL" fieldClassName={shared.span2} mono value={form.endpoint} error={errors.endpoint_url} disabled={busy} onChange={(e) => edit({ endpoint: e.target.value })} />
        <Input label="超时（毫秒）" mono inputMode="numeric" value={form.timeoutMs} error={errors.timeout_ms} hint="500–30000" disabled={busy} onChange={(e) => edit({ timeoutMs: e.target.value })} />
        <Input label="最多尝试次数" mono inputMode="numeric" value={form.maxAttempts} error={errors.max_attempts} hint="1–10，失败后退避重试" disabled={busy} onChange={(e) => edit({ maxAttempts: e.target.value })} />

        <div className={`${shared.span2} ${css.section}`}>订阅事件</div>
        <div className={shared.span2}>
          <div className={css.eventSwitches}>
            {events.map((e) => (
              <Switch
                key={e.name}
                label={e.desc}
                title={e.name}
                checked={form.events.includes(e.name)}
                disabled={busy}
                onChange={(ev) => edit({ events: ev.target.checked ? events.filter((x) => x.name === e.name || form.events.includes(x.name)).map((x) => x.name) : form.events.filter((x) => x !== e.name) })}
              />
            ))}
          </div>
          {errors.events && (
            <div className={shared.error} role="alert">
              {errors.events}
            </div>
          )}
        </div>

        <div className={`${shared.span2} ${css.section}`}>安全</div>
        <Input
          label="更换签名密钥（可选）"
          fieldClassName={shared.span2}
          type="password"
          autoComplete="off"
          mono
          value={form.secret}
          placeholder={hook.has_secret ? '已设置，留空不修改' : '未设置'}
          hint="填写后立即替换，插件那边要同步改成新密钥"
          disabled={busy}
          onChange={(e) => edit({ secret: e.target.value })}
        />
        <div className={shared.span2}>
          <Switch label="启用" checked={form.enabled} disabled={busy} onChange={(e) => edit({ enabled: e.target.checked })} />
        </div>
      </div>
    </Modal>
  )
}

/** 一次性签名密钥弹窗：不可点遮罩关闭，先复制再关 */
export function SecretModal({ secret, onClose }: { secret: { secret: string; hint: string } | null; onClose: () => void }) {
  const toast = useToast()
  const copy = () =>
    navigator.clipboard.writeText(secret?.secret ?? '').then(
      () => toast('签名密钥已复制'),
      () => toast('复制失败，请手动选中复制', 'danger'),
    )
  return (
    <Modal
      open={secret !== null}
      onClose={onClose}
      dismissible={false}
      size={480}
      eyebrow="仅此一次可见"
      title="钩子已创建"
      actions={
        <>
          <Button size="dialog" onClick={onClose}>
            关闭
          </Button>
          <Button size="dialog" variant="primary" onClick={copy}>
            复制密钥
          </Button>
        </>
      }
    >
      <div className={shared.stack}>
        <p className={shared.faint}>{secret?.hint || '签名密钥只显示这一次，请立刻填进插件那边的配置'}。面板用它对每次投递签名，插件据此验签；库里只存密文，关掉后谁都读不回来。</p>
        <code className={css.secret}>{secret?.secret}</code>
      </div>
    </Modal>
  )
}
