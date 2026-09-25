/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../ui，依赖 ./hookActions 的 useSaveHook，依赖 ./logic 的钩子表单函数，依赖 ./schemas 的 Hook / HookEvent，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 EventPicker（订阅事件多选菜单）、HookModal（编辑钩子）、SecretModal（一次性签名密钥）
 * [POS]: admin/screens/system 钩子的弹层与选择器：EventPicker 按后端事件目录列开关（带中文说明），首项「全部事件」一次选满；HookModal 是契约待补·前端的编辑（名称、说明、地址、事件、超时 500–30000 毫秒、最多尝试 1–10 次、启用、更换密钥），按 code upsert 全字段回填——超时与次数后端只有 DB CHECK，越界会变 500，所以前端先拦；
 *        SecretModal 展示响应里一次性的签名密钥（库里只存密文，之后谁都读不回），可复制，关掉就再也看不到
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { Button, Checkbox, Input, Menu, Modal, Switch, useToast, type MenuEntry } from '../../../ui'
import { useSaveHook } from './hookActions'
import { eventsLabel, hookBody, hookFormFrom, validateHook, type HookForm } from './logic'
import type { Hook, HookEvent } from './schemas'
import css from './system.module.css'

export function EventPicker({ events, value, onChange, disabled }: { events: HookEvent[]; value: string[]; onChange: (v: string[]) => void; disabled?: boolean }) {
  const label = eventsLabel(value, events.length)
  if (disabled) return <span className={css.picker}>{label}</span>
  const entries: MenuEntry[] = [
    { key: 'all', label: '全部事件', current: value.length === events.length, onSelect: () => onChange(events.map((e) => e.name)) },
    { kind: 'separator', key: 's' },
    ...events.map((e): MenuEntry => ({
      kind: 'toggle',
      key: e.name,
      label: `${e.name} · ${e.desc}`,
      checked: value.includes(e.name),
      onChange: (on) => onChange(on ? events.filter((x) => x.name === e.name || value.includes(x.name)).map((x) => x.name) : value.filter((x) => x !== e.name)),
    })),
  ]
  return <Menu className={css.pickerRoot} label="订阅事件" align="start" triggerLabel={`订阅事件：${label}`} triggerClassName={css.picker} menuClassName={css.pickerMenu} trigger={<span className={css.pickerText}>{label}</span>} entries={entries} />
}

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
      size="lg"
      onClose={onClose}
      title={`编辑钩子「${hook.name}」`}
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
      <div className={css.formGrid}>
        <Input label="名称" value={form.name} error={errors.name} disabled={busy} onChange={(e) => edit({ name: e.target.value })} />
        <Input label="说明（可选）" value={form.description} disabled={busy} onChange={(e) => edit({ description: e.target.value })} />
        <Input label="端点 URL" fieldClassName={css.span2} mono value={form.endpoint} error={errors.endpoint_url} disabled={busy} onChange={(e) => edit({ endpoint: e.target.value })} />
        <Input label="超时（毫秒）" mono inputMode="numeric" value={form.timeoutMs} error={errors.timeout_ms} hint="500–30000" disabled={busy} onChange={(e) => edit({ timeoutMs: e.target.value })} />
        <Input label="最多尝试次数" mono inputMode="numeric" value={form.maxAttempts} error={errors.max_attempts} hint="1–10，失败后退避重试" disabled={busy} onChange={(e) => edit({ maxAttempts: e.target.value })} />
        <div className={css.span2}>
          <div className={css.label}>订阅事件</div>
          <div className={css.eventChecks}>
            {events.map((e) => (
              <Checkbox
                key={e.name}
                label={`${e.name} · ${e.desc}`}
                checked={form.events.includes(e.name)}
                disabled={busy}
                onChange={(ev) => edit({ events: ev.target.checked ? events.filter((x) => x.name === e.name || form.events.includes(x.name)).map((x) => x.name) : form.events.filter((x) => x !== e.name) })}
              />
            ))}
          </div>
          {errors.events && (
            <div className={css.error} role="alert">
              {errors.events}
            </div>
          )}
        </div>
        <Input
          label="更换签名密钥（可选）"
          fieldClassName={css.span2}
          type="password"
          autoComplete="off"
          mono
          value={form.secret}
          placeholder={hook.has_secret ? '已设置，留空不修改' : '未设置'}
          hint="填写后立即替换，插件那边要同步改成新密钥"
          disabled={busy}
          onChange={(e) => edit({ secret: e.target.value })}
        />
        <div className={css.span2}>
          <Switch label="启用" checked={form.enabled} disabled={busy} onChange={(e) => edit({ enabled: e.target.checked })} />
        </div>
      </div>
    </Modal>
  )
}

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
      title="签名密钥（仅此一次可见）"
      actions={
        <>
          <Button size="dialog" onClick={copy}>
            复制
          </Button>
          <Button size="dialog" variant="primary" onClick={onClose}>
            我已保存
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <div className={css.faint}>{secret?.hint || '签名密钥只显示这一次，请立刻填进插件那边的配置'}。面板用它对每次投递签名，插件据此验签；库里只存密文，关掉后谁都读不回来。</div>
        <code className={css.secret}>{secret?.secret}</code>
      </div>
    </Modal>
  )
}
