import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { Button, Input, QueryView, Switch, TextArea, useToast } from '../../../ui'
import { ChannelCard } from './ChannelCard'
import { parseChatId } from './logic'
import { SK, useFailure, useInvalidateSystem, useTelegramSettings } from './queries'
import css from './system.module.css'

const extrasSchema = z.object({
  sub_command_enabled: z.boolean(),
  start_message: z.string(),
  admin_paid_notify: z.boolean(),
  admin_ticket_notify: z.boolean(),
  join_review_enabled: z.boolean(),
  user_group_chat_id: z.number().nullable(),
  discuss_link: z.string(),
})

type Extras = z.output<typeof extrasSchema>

type ExtrasDraft = {
  sub_command_enabled: boolean
  start_message: string
  admin_paid_notify: boolean
  admin_ticket_notify: boolean
  join_review_enabled: boolean
  userGroupChatId: string
  discuss_link: string
}

function formFrom(saved: Extras): ExtrasDraft {
  return {
    sub_command_enabled: saved.sub_command_enabled,
    start_message: saved.start_message,
    admin_paid_notify: saved.admin_paid_notify,
    admin_ticket_notify: saved.admin_ticket_notify,
    join_review_enabled: saved.join_review_enabled,
    userGroupChatId: saved.user_group_chat_id === null ? '' : String(saved.user_group_chat_id),
    discuss_link: saved.discuss_link,
  }
}

function bodyFrom(form: ExtrasDraft): Extras | { error: Record<string, string> } {
  const errors: Record<string, string> = {}
  const chatRaw = form.userGroupChatId.trim()
  let user_group_chat_id: number | null = null
  if (!chatRaw) user_group_chat_id = null
  else {
    const chat = parseChatId(chatRaw)
    if (!chat.ok) errors.user_group_chat_id = '用户群 chat id 必须是非 0 整数'
    else user_group_chat_id = chat.value
  }
  if (Object.keys(errors).length) return { error: errors }
  return {
    sub_command_enabled: form.sub_command_enabled,
    start_message: form.start_message,
    admin_paid_notify: form.admin_paid_notify,
    admin_ticket_notify: form.admin_ticket_notify,
    join_review_enabled: form.join_review_enabled,
    user_group_chat_id,
    discuss_link: form.discuss_link,
  }
}

export function TelegramExtrasCard() {
  const api = useApi()
  const telegram = useTelegramSettings()
  const q = useQuery({ queryKey: [...SK, 'telegram-extras'], queryFn: ({ signal }) => api.get('v1/settings/telegram/extras', extrasSchema, { signal }) })
  return (
    <QueryView query={q} rows={4} empty={null}>
      {(saved) => <ExtrasEditor saved={saved} telegramEnabled={telegram.data?.enabled === true} />}
    </QueryView>
  )
}

function ExtrasEditor({ saved, telegramEnabled }: { saved: Extras; telegramEnabled: boolean }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<ExtrasDraft | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const form = draft ?? formFrom(saved)
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(formFrom(saved))
  const edit = (patch: Partial<ExtrasDraft>) => setDraft({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (body: Extras) => api.post('v1/settings/telegram/extras', extrasSchema, { body }),
    onSuccess: () => {
      toast('Telegram 扩展设置已保存')
      void invalidate('telegram-extras').then(() => setDraft(null))
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const submit = () => {
    const body = bodyFrom(form)
    if ('error' in body) return setErrors(body.error)
    setErrors({})
    save.mutate(body)
  }

  const disabled = save.isPending
  return (
    <ChannelCard
      title="Telegram · 机器人与群组"
      status={{ label: '扩展设置', tone: 'muted' }}
      foot={
        <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      }
    >
      <div className={css.span2}>
        <Switch
          label="/sub 命令：在 Telegram 里发订阅链接"
          checked={form.sub_command_enabled}
          disabled={disabled}
          onChange={(e) => edit({ sub_command_enabled: e.target.checked })}
        />
        <div className={css.faint}>订阅链接等同于账号凭据，私聊里发出后谁拿到都能用；缺省关闭</div>
      </div>
      <div className={css.span2}>
        <TextArea
          label="/start 欢迎文案"
          placeholder="留空使用内置欢迎语"
          hint="用户直接发 /start（不带绑定码）时回复这段；最多 1000 字"
          rows={3}
          value={form.start_message}
          error={errors.start_message}
          disabled={disabled}
          onChange={(e) => edit({ start_message: e.target.value })}
        />
      </div>
      <div className={css.span2}>
        <Switch label="收款提醒发到管理员群组" checked={form.admin_paid_notify} disabled={disabled} onChange={(e) => edit({ admin_paid_notify: e.target.checked })} />
      </div>
      <div className={css.span2}>
        <Switch
          label="工单提醒发到管理员群组"
          checked={form.admin_ticket_notify}
          disabled={disabled}
          onChange={(e) => edit({ admin_ticket_notify: e.target.checked })}
        />
        <div className={css.faint}>新工单与用户回复都会推到管理员群组；已绑定 Telegram 的管理员直接回复提醒消息即可回复工单（侧栏账户菜单 → 绑定 Telegram）</div>
      </div>
      <div className={css.span2}>
        <Switch
          label="入群审核"
          checked={form.join_review_enabled}
          disabled={disabled}
          onChange={(e) => edit({ join_review_enabled: e.target.checked })}
        />
        <div className={css.faint}>用户申请加入下面的用户群时，已绑定账号且有有效订阅的自动通过，否则拒绝。群要开启「申请加入」，bot 要是群管理员并有邀请成员权限</div>
      </div>
      <Input
        label="用户群 Chat ID"
        mono
        inputMode="numeric"
        placeholder="如 -1001234567890"
        value={form.userGroupChatId}
        error={errors.user_group_chat_id}
        disabled={disabled}
        onChange={(e) => edit({ userGroupChatId: e.target.value })}
      />
      <Input
        label="讨论群链接"
        placeholder="https://t.me/…"
        hint="填了以后门户「账号 → Telegram」显示这个入口"
        value={form.discuss_link}
        error={errors.discuss_link}
        disabled={disabled}
        onChange={(e) => edit({ discuss_link: e.target.value })}
      />
      {!telegramEnabled && <div className={`${css.span2} ${css.faint}`}>Telegram 通知未启用，这些设置要启用后才生效</div>}
    </ChannelCard>
  )
}
