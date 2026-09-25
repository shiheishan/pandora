/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/router 的 href，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./ChannelCard，依赖 ./logic 的邮件函数，依赖 ./queries，依赖 ./schemas，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 ChannelsTab（通知与插件 · 通知渠道标签）
 * [POS]: admin/screens/system 的通知渠道（设计稿 t_notify）：邮件 · SMTP 卡（设计缺的加密方式下拉、清除已存密码补上）、契约待补·前端的「注册与验证」卡（注册模式 + 邮箱验证，注明还受降级开关控制）、Telegram 卡在 TelegramCard.tsx。
 *        保存都是 platform.settings.write、无 reauth 无幂等；SMTP 六个字段每次整体覆盖，注册卡用同一接口、带「已保存」的 SMTP 字段而不是 SMTP 卡里没保存的输入。测试发送（ops.notification.write）用的是已保存的配置，有未保存的修改时先禁用并提示保存。
 *        表单草稿为 null 时跟着服务端数据走，保存成功后置回 null
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, QueryView, Segmented, Select, Switch, useToast } from '../../../ui'
import { REGISTRATION_LABEL, registrationBody, smtpBody, smtpFormFrom, smtpStatus, validateSmtp, type SmtpForm, type Tone } from './logic'
import { useCan, useFailure, useInvalidateSystem, useMailSettings } from './queries'
import { ENCRYPTIONS, mailTested, okResponse, REGISTRATION_MODES, type MailSettings, type RegistrationMode } from './schemas'
import { ChannelCard } from './ChannelCard'
import css from './system.module.css'
import { TelegramCard } from './TelegramCard'

export function ChannelsTab() {
  const mail = useMailSettings()
  return (
    <div className={css.cards}>
      <QueryView query={mail} rows={4} empty={null}>
        {(saved) => <SmtpCard saved={saved} />}
      </QueryView>
      <TelegramCard />
      <QueryView query={mail} rows={3} empty={null}>
        {(saved) => <RegistrationCard saved={saved} />}
      </QueryView>
    </div>
  )
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function SmtpCard({ saved }: { saved: MailSettings }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<SmtpForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [to, setTo] = useState('')
  const form = draft ?? smtpFormFrom(saved)
  const dirty = draft !== null && !same(draft, smtpFormFrom(saved))
  const writable = can('platform.settings.write')
  const edit = (patch: Partial<SmtpForm>) => setDraft({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (f: SmtpForm) => api.post('v1/settings/mail', okResponse, { body: smtpBody(f) }),
    onSuccess: () => {
      toast('SMTP 设置已保存')
      void invalidate('mail').then(() => setDraft(null))
    },
    onError: (error) => fail(error, (f) => setErrors(f)),
  })
  const test = useMutation({
    mutationFn: (address: string) => api.post('v1/settings/mail/test', mailTested, { body: { to: address } }),
    onSuccess: (r) => toast(`测试邮件已发送至 ${r.to}`),
    onError: (error) => fail(error, (f) => setErrors(f)),
  })

  const submit = () => {
    const errs = validateSmtp(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(form)
  }

  const disabled = !writable || save.isPending
  return (
    <ChannelCard
      title="邮件 · SMTP"
      status={smtpStatus(saved)}
      foot={
        <>
          <Input size="sm" aria-label="测试收件地址" fieldClassName={css.grow} placeholder="测试收件地址" value={to} error={errors.to} onChange={(e) => setTo(e.target.value)} />
          {can('ops.notification.write') && (
            <Button size="sm" disabled={dirty || !to.trim()} busy={test.isPending} title={dirty ? '测试用的是已保存的配置，请先保存' : undefined} onClick={() => test.mutate(to.trim())}>
              发送测试
            </Button>
          )}
          {writable && (
            <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
              保存
            </Button>
          )}
        </>
      }
    >
      <Input label="服务器" mono value={form.host} placeholder="smtp.example.com" disabled={disabled} onChange={(e) => edit({ host: e.target.value })} />
      <div className={css.pair}>
        <Input label="端口" mono inputMode="numeric" value={form.port} error={errors.smtp_port} disabled={disabled} onChange={(e) => edit({ port: e.target.value })} />
        <Select label="加密" value={form.encryption} disabled={disabled} options={ENCRYPTIONS.map((v) => ({ value: v, label: v === 'ssl' ? 'SSL' : v === 'tls' ? 'STARTTLS' : '不加密' }))} onChange={(e) => edit({ encryption: e.target.value as SmtpForm['encryption'] })} />
      </div>
      <Input label="用户名" mono value={form.username} disabled={disabled} onChange={(e) => edit({ username: e.target.value })} />
      <div>
        <Input label="密码" type="password" autoComplete="new-password" value={form.password} placeholder={saved.has_password ? '已设置，留空不修改' : '未设置'} disabled={disabled || form.clearPassword} onChange={(e) => edit({ password: e.target.value })} />
        {saved.has_password && writable && <Checkbox className={css.inlineCheck} label="清除已存密码" checked={form.clearPassword} disabled={disabled} onChange={(e) => edit({ clearPassword: e.target.checked, password: '' })} />}
      </div>
      <Input label="发件人" fieldClassName={css.span2} mono value={form.sender} error={errors.from_address} placeholder="Pandora <noreply@example.com>" disabled={disabled} onChange={(e) => edit({ sender: e.target.value })} />
      {dirty && <div className={`${css.span2} ${css.faint}`}>有未保存的修改；发送测试用的是已保存的配置，保存后再测。</div>}
    </ChannelCard>
  )
}

function RegistrationCard({ saved }: { saved: MailSettings }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<{ mode: RegistrationMode; verify: boolean } | null>(null)
  const [error, setError] = useState('')
  const value = draft ?? { mode: saved.registration_mode, verify: saved.email_verification }
  const dirty = draft !== null && (draft.mode !== saved.registration_mode || draft.verify !== saved.email_verification)
  const writable = can('platform.settings.write')

  const save = useMutation({
    mutationFn: (v: { mode: RegistrationMode; verify: boolean }) => api.post('v1/settings/mail', okResponse, { body: registrationBody(saved, v.mode, v.verify) }),
    onSuccess: () => {
      toast('注册设置已保存')
      void invalidate('mail').then(() => setDraft(null))
    },
    onError: (e) => fail(e, (f) => setError(f.email_verification ?? f.registration_mode ?? Object.values(f)[0] ?? '')),
  })

  const submit = () => {
    if (value.verify && (!saved.smtp_host || !saved.from_address)) return setError('开启邮箱验证前请先在 SMTP 卡里填好服务器与发件人地址并保存')
    setError('')
    save.mutate(value)
  }

  const status = { label: `${REGISTRATION_LABEL[saved.registration_mode]}注册${saved.email_verification ? ' · 需验证邮箱' : ''}`, tone: (saved.registration_mode === 'closed' ? 'muted' : 'ok') as Tone }
  return (
    <ChannelCard
      title="注册与验证"
      status={status}
      foot={
        <>
          <span className={`${css.grow} ${css.faint}`}>与 SMTP 设置同一份配置，保存时带上已保存的 SMTP 字段</span>
          {writable && (
            <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
              保存
            </Button>
          )}
        </>
      }
    >
      <div className={css.span2}>
        <div className={css.label}>注册模式</div>
        <Segmented label="注册模式" value={value.mode} options={REGISTRATION_MODES.map((m) => ({ value: m, label: REGISTRATION_LABEL[m] }))} onChange={(mode) => writable && setDraft({ ...value, mode })} />
      </div>
      <div className={css.span2}>
        <Switch label="注册时验证邮箱（发送验证码）" checked={value.verify} disabled={!writable || save.isPending} onChange={(e) => setDraft({ ...value, verify: e.target.checked })} />
        {error && (
          <div className={css.error} role="alert">
            {error}
          </div>
        )}
      </div>
      <div className={`${css.span2} ${css.note}`}>
        实际能否注册还受降级开关控制：「auth.registration」关闭（缺行也算关闭）时一律不能注册；「notify.email」关闭时验证码发不出去，开了邮箱验证的注册也走不通。开关在 <a href={href('/security/switches')}>安全与运维 · 降级开关</a>。
      </div>
    </ChannelCard>
  )
}
