import { useMutation } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, Modal, QueryView, Segmented, Select, Switch, useToast } from '../../../ui'
import { ChannelCard, StatusChip } from './ChannelCard'
import { adminAlertBody, formatSender, REGISTRATION_LABEL, registrationBody, smtpBody, smtpFormFrom, smtpStatus, validateSmtp, type AdminAlert, type SmtpForm, type Tone } from './logic'
import { useFailure, useInvalidateSystem, useMailSettings, useTelegramExtras, useTelegramSettings } from './queries'
import { ENCRYPTIONS, mailTested, okResponse, REGISTRATION_MODES, telegramExtrasSchema, type RegistrationMode, type TelegramExtras } from './schemas'
import css from './notify.module.css'
import sys from './system.module.css'
import { TelegramDialog } from './TelegramCard'

export function ChannelsTab() {
  const mail = useMailSettings()
  const telegram = useTelegramSettings()
  const extras = useTelegramExtras()
  const [open, setOpen] = useState<'mail' | 'telegram' | 'registration' | null>(null)
  const [dirty, setDirty] = useState({ mail: false, telegram: false, registration: false })
  const close = () => setOpen(null)
  /** 弹窗回报未保存状态，渠道卡上显示「未保存」；回调要稳定，否则弹窗的 effect 每次渲染都会重跑 */
  const markDirty = useMemo(() => {
    const make = (key: 'mail' | 'telegram' | 'registration') => (value: boolean) => setDirty((d) => (d[key] === value ? d : { ...d, [key]: value }))
    return { mail: make('mail'), telegram: make('telegram'), registration: make('registration') }
  }, [])

  const tg = telegram.data
  const tgOn = tg?.enabled ?? false
  const tgTag = !tg || !tg.has_token ? { label: '未配置', tone: 'muted' as Tone } : tg.enabled ? { label: '已启用', tone: 'ok' as Tone } : { label: '已停用', tone: 'warn' as Tone }
  const tgParts = tg?.enabled ? [tg.bot_username ? `Bot @${tg.bot_username}` : '', tg.admin_chat_id !== null ? `管理群 ${tg.admin_chat_id}` : ''].filter(Boolean) : []
  const tgDesc = tgParts.length > 0 ? tgParts.join(' · ') : '启用后用户和管理员都能在 Telegram 收到通知'

  return (
    <div className={css.page}>
      <section className={css.section}>
        <h3 className={css.sectionTitle}>发送渠道</h3>
        <QueryView query={mail} rows={3} empty={null}>
          {(saved) => {
            const status = smtpStatus(saved)
            const configured = status.tone === 'ok'
            return (
              <div className={css.chList}>
                <ChannelCard
                  icon={<ChannelIcon paths={ICONS.inapp} />}
                  title="站内信"
                  tag={{ label: '始终开启', tone: 'ok' }}
                  desc="显示在用户端和后台右上角的铃铛里，无需配置"
                />
                <ChannelCard
                  icon={<ChannelIcon paths={ICONS.email} />}
                  title="邮件"
                  tag={{ label: configured ? '已配置' : '未配置', tone: configured ? 'ok' : 'muted' }}
                  dirty={dirty.mail}
                  desc={configured ? `经 ${saved.smtp_host} 发出 · 发件人 ${formatSender(saved.from_name, saved.from_address)}` : '配好 SMTP 服务器后才能发邮件'}
                  onOpen={() => setOpen('mail')}
                />
                <ChannelCard
                  icon={<ChannelIcon paths={ICONS.tg} />}
                  title="Telegram"
                  tag={tgTag}
                  dirty={dirty.telegram}
                  desc={tgDesc}
                  onOpen={() => setOpen('telegram')}
                />
              </div>
            )
          }}
        </QueryView>
      </section>

      <section className={css.section}>
        <h3 className={css.sectionTitle}>管理员提醒</h3>
        <QueryView query={extras} rows={2} empty={null}>
          {(saved) => <AdminAlerts saved={saved} enabled={tgOn} />}
        </QueryView>
      </section>

      <section className={css.section}>
        <h3 className={css.sectionTitle}>注册与验证</h3>
        <QueryView query={mail} rows={1} empty={null}>
          {(saved) => (
            <ChannelCard
              title="注册模式与邮箱验证"
              dirty={dirty.registration}
              desc={`${REGISTRATION_LABEL[saved.registration_mode]}注册${saved.email_verification ? ' · 需验证邮箱' : ''}`}
              onOpen={() => setOpen('registration')}
            />
          )}
        </QueryView>
      </section>

      {/* 弹窗一直挂载：关掉不丢草稿，草稿状态由 onDirtyChange 回报给渠道卡 */}
      <MailDialog open={open === 'mail'} onClose={close} onDirtyChange={markDirty.mail} />
      <TelegramDialog open={open === 'telegram'} onClose={close} onDirtyChange={markDirty.telegram} />
      <RegistrationDialog open={open === 'registration'} onClose={close} onDirtyChange={markDirty.registration} />
    </div>
  )
}

/** 渠道卡图标（18px 线条 svg，路径照设计稿） */
const ICONS: Record<'inapp' | 'email' | 'tg', readonly string[]> = {
  inapp: ['M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9', 'M10.3 21a1.94 1.94 0 0 0 3.4 0'],
  email: ['M4 4h16c1.1 0 2 .9 2 2v12c0 1.1-.9 2-2 2H4c-1.1 0-2-.9-2-2V6c0-1.1.9-2 2-2z', 'm22 6-10 7L2 6'],
  tg: ['m22 2-7 20-4-9-9-4Z', 'M22 2 11 13'],
}

function ChannelIcon({ paths }: { paths: readonly string[] }) {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {paths.map((d) => (
        <path key={d} d={d} />
      ))}
    </svg>
  )
}

// ---------------------------------------------------------------------------
// 管理员提醒：两个开关即时保存（Telegram 未启用时禁用并显示为关）
// ---------------------------------------------------------------------------
const ALERT_ROWS: { key: AdminAlert; label: string; desc: string }[] = [
  { key: 'admin_paid_notify', label: '新订单已支付', desc: '用户付款成功后推送订单号与金额' },
  { key: 'admin_ticket_notify', label: '工单提醒', desc: '新工单与用户回复都推送到管理员群组；已绑定 Telegram 的管理员直接回复提醒消息即可回复工单' },
]

function AdminAlerts({ saved, enabled }: { saved: TelegramExtras; enabled: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const save = useMutation({
    mutationFn: (p: { key: AdminAlert; on: boolean }) => api.post('v1/settings/telegram/extras', telegramExtrasSchema, { body: adminAlertBody(saved, p.key, p.on) }),
    onSuccess: (_r, p) => {
      const label = ALERT_ROWS.find((row) => row.key === p.key)?.label ?? p.key
      toast(`${p.on ? '已开启' : '已关闭'}「${label}」提醒`)
      void invalidate('telegram-extras')
    },
    onError: (e) => fail(e),
  })

  return (
    <>
      <div className={css.alerts}>
        {ALERT_ROWS.map((row) => (
          <div key={row.key} className={enabled ? css.alertRow : `${css.alertRow} ${css.alertDim}`}>
            <span className={css.alertMain}>
              <span className={css.alertLabel}>{row.label}</span>
              <span className={css.alertDesc}>{row.desc}</span>
            </span>
            <Switch aria-label={row.label} checked={enabled && saved[row.key]} disabled={!enabled || save.isPending} onChange={(e) => save.mutate({ key: row.key, on: e.target.checked })} />
          </div>
        ))}
      </div>
      {!enabled && <span className={css.warnText}>Telegram 未启用，先在上面配好并启用才能打开这些提醒。</span>}
    </>
  )
}

// ---------------------------------------------------------------------------
// 邮件弹窗（原 SMTP 卡）
// ---------------------------------------------------------------------------
interface DialogProps {
  open: boolean
  onClose: () => void
  onDirtyChange: (dirty: boolean) => void
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

function MailDialog({ open, onClose, onDirtyChange }: DialogProps) {
  const mail = useMailSettings()
  const saved = mail.data
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<SmtpForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [to, setTo] = useState('')
  const form = saved ? (draft ?? smtpFormFrom(saved)) : null
  const dirty = saved !== undefined && draft !== null && !same(draft, smtpFormFrom(saved))
  const edit = (patch: Partial<SmtpForm>) => form !== null && setDraft({ ...form, ...patch })

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

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
    if (form === null) return
    const errs = validateSmtp(form)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(form)
  }

  if (saved === undefined || form === null) return null
  const status = smtpStatus(saved)
  const disabled = save.isPending
  return (
    <Modal
      open={open}
      onClose={onClose}
      size={640}
      divided
      title="SMTP 邮件"
      headExtra={<StatusChip label={status.label} tone={status.tone} />}
      footerStart={
        <>
          <Input aria-label="测试收件地址" fieldClassName={sys.grow} placeholder="测试收件地址" value={to} error={errors.to} onChange={(e) => setTo(e.target.value)} />
          <Button size="dialog" disabled={dirty || !to.trim()} busy={test.isPending} title={dirty ? '测试用的是已保存的配置，请先保存' : undefined} onClick={() => test.mutate(to.trim())}>
            发送测试
          </Button>
        </>
      }
      actions={
        <Button size="dialog" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      }
    >
      <div className={css.mailGrid}>
        <Input label="服务器" mono value={form.host} error={errors.smtp_host} placeholder="smtp.example.com" disabled={disabled} onChange={(e) => edit({ host: e.target.value })} />
        <Input label="端口" mono inputMode="numeric" value={form.port} error={errors.smtp_port} disabled={disabled} onChange={(e) => edit({ port: e.target.value })} />
        <Select label="加密" value={form.encryption} disabled={disabled} options={ENCRYPTIONS.map((v) => ({ value: v, label: v === 'ssl' ? 'SSL' : v === 'tls' ? 'STARTTLS' : '不加密' }))} onChange={(e) => edit({ encryption: e.target.value as SmtpForm['encryption'] })} />
        <Input label="用户名" mono value={form.username} error={errors.smtp_username} disabled={disabled} onChange={(e) => edit({ username: e.target.value })} />
        <div className={sys.span2}>
          <Input label="密码" type="password" autoComplete="new-password" value={form.password} error={errors.smtp_password} placeholder={saved.has_password ? '已设置，留空不修改' : '未设置'} disabled={disabled || form.clearPassword} onChange={(e) => edit({ password: e.target.value })} />
          {saved.has_password && <Checkbox className={sys.inlineCheck} label="清除已存密码" checked={form.clearPassword} disabled={disabled} onChange={(e) => edit({ clearPassword: e.target.checked, password: '' })} />}
        </div>
        <Input label="发件人" fieldClassName={sys.span2} mono value={form.sender} error={errors.from_address ?? errors.from_name} placeholder="Pandora <noreply@example.com>" disabled={disabled} onChange={(e) => edit({ sender: e.target.value })} />
        {dirty && <p className={`${sys.span2} ${css.dirtyNote}`}>有未保存的修改；发送测试用的是已保存的配置，保存后再测。</p>}
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// 注册弹窗（原注册卡）
// ---------------------------------------------------------------------------
function RegistrationDialog({ open, onClose, onDirtyChange }: DialogProps) {
  const mail = useMailSettings()
  const saved = mail.data
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<{ mode: RegistrationMode; verify: boolean } | null>(null)
  const [error, setError] = useState('')
  const value = saved ? (draft ?? { mode: saved.registration_mode, verify: saved.email_verification }) : null
  const dirty = saved !== undefined && draft !== null && (draft.mode !== saved.registration_mode || draft.verify !== saved.email_verification)

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  const save = useMutation({
    mutationFn: (v: { mode: RegistrationMode; verify: boolean }) => {
      if (saved === undefined) throw new Error('设置还没加载')
      return api.post('v1/settings/mail', okResponse, { body: registrationBody(saved, v.mode, v.verify) })
    },
    onSuccess: () => {
      toast('注册设置已保存')
      void invalidate('mail').then(() => setDraft(null))
    },
    onError: (e) => fail(e, (f) => setError(f.email_verification ?? f.registration_mode ?? Object.values(f)[0] ?? '')),
  })

  const submit = () => {
    if (value === null || saved === undefined) return
    if (value.verify && (!saved.smtp_host || !saved.from_address)) return setError('开启邮箱验证前请先在「SMTP 邮件」弹窗里填好服务器与发件人地址并保存')
    setError('')
    save.mutate(value)
  }

  if (saved === undefined || value === null) return null
  const status = { label: `${REGISTRATION_LABEL[saved.registration_mode]}注册${saved.email_verification ? ' · 需验证邮箱' : ''}`, tone: (saved.registration_mode === 'closed' ? 'muted' : 'ok') as Tone }
  return (
    <Modal
      open={open}
      onClose={onClose}
      size={640}
      divided
      title="注册与验证"
      headExtra={<StatusChip label={status.label} tone={status.tone} />}
      actions={
        <Button size="dialog" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      }
    >
      <p className={css.notePlain}>
        实际能否注册还受降级开关控制：「auth.registration」关闭时一律不能注册；「notify.email」关闭时验证码发不出去。开关在 <a href={href('/security/switches')}>安全与运维 · 降级开关</a>。
      </p>
      <div className={css.regRow}>
        <span className={css.regLabel}>注册模式</span>
        <Segmented label="注册模式" value={value.mode} options={REGISTRATION_MODES.map((m) => ({ value: m, label: REGISTRATION_LABEL[m] }))} onChange={(mode) => setDraft({ ...value, mode })} />
      </div>
      <div>
        <Switch label="注册时验证邮箱（发送验证码）" checked={value.verify} disabled={save.isPending} onChange={(e) => setDraft({ ...value, verify: e.target.checked })} />
        {error && (
          <div className={sys.error} role="alert">
            {error}
          </div>
        )}
      </div>
    </Modal>
  )
}
