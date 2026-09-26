/**
 * [INPUT]: 依赖 react 的 useState，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./ChannelCard，依赖 ./logic 的 Telegram 函数，依赖 ./queries，依赖 ./schemas，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 TelegramCard（通知渠道里的 Telegram 卡）
 * [POS]: admin/screens/system 通知渠道的 Telegram 卡（设计稿 channels[1]）：标题栏状态（已启用 @bot / 已停用 / 未配置 Token，后端不做连通性探测）与契约待补·前端的「启用」开关；Bot Token 只进不出（留空不修改），管理员群组 chat id 按 D-A-4（已决，5.A.2）只作测试的默认目标（没改不提交、清空提交 null）。
 *        保存 platform.settings.write、无 reauth 无幂等；测试 ops.notification.write，用的是已保存的配置，chat id 留空发往管理员群组，有未保存修改时先禁用
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, QueryView, Switch, useToast } from '../../../ui'
import { ChannelCard } from './ChannelCard'
import { parseChatId, telegramBody, telegramFormFrom, telegramStatus, type TelegramForm } from './logic'
import { useCan, useFailure, useInvalidateSystem, useTelegramSettings } from './queries'
import { telegramSaved, telegramTested, type TelegramSettings } from './schemas'
import css from './system.module.css'

export function TelegramCard() {
  const settings = useTelegramSettings()
  return (
    <QueryView query={settings} rows={3} empty={null}>
      {(saved) => <TelegramForm saved={saved} />}
    </QueryView>
  )
}

function TelegramForm({ saved }: { saved: TelegramSettings }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  const [draft, setDraft] = useState<TelegramForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [to, setTo] = useState('')
  const form = draft ?? telegramFormFrom(saved)
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(telegramFormFrom(saved))
  const writable = can('platform.settings.write')
  const edit = (patch: Partial<TelegramForm>) => setDraft({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/settings/telegram', telegramSaved, { body }),
    onSuccess: () => {
      toast('Telegram 设置已保存')
      void invalidate('telegram').then(() => setDraft(null))
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })
  const test = useMutation({
    mutationFn: (chat: number | null) => api.post('v1/settings/telegram/test', telegramTested, { body: chat === null ? {} : { chat_id: chat } }),
    onSuccess: () => toast('Telegram 测试消息已发送'),
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const submit = () => {
    const body = telegramBody(form, saved)
    if ('error' in body) return setErrors({ admin_chat_id: body.error })
    if (form.enabled && !body.bot_username) return setErrors({ bot_username: '启用前要填 Bot 用户名，用户要靠它找到你的 bot' })
    setErrors({})
    save.mutate(body)
  }

  const sendTest = () => {
    const chat = parseChatId(to)
    if (!chat.ok) return setErrors({ chat_id: 'chat id 必须是非 0 整数' })
    if (chat.value === null && saved.admin_chat_id === null) return setErrors({ chat_id: '请填写要接收测试消息的 chat id，或先保存管理员群组 chat id' })
    setErrors({})
    test.mutate(chat.value)
  }

  const disabled = !writable || save.isPending
  return (
    <ChannelCard
      title="Telegram"
      status={telegramStatus(saved)}
      foot={
        <>
          <Input size="sm" aria-label="测试 chat id" mono fieldClassName={css.grow} placeholder={saved.admin_chat_id === null ? '接收测试的 chat id' : '留空发送到管理员群组'} value={to} error={errors.chat_id} onChange={(e) => setTo(e.target.value)} />
          {can('ops.notification.write') && (
            <Button size="sm" disabled={dirty} busy={test.isPending} title={dirty ? '测试用的是已保存的配置，请先保存' : undefined} onClick={sendTest}>
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
      <div className={css.span2}>
        <Switch label="启用 Telegram 通知" checked={form.enabled} disabled={disabled} onChange={(e) => edit({ enabled: e.target.checked })} />
      </div>
      <Input label="Bot Token" fieldClassName={css.span2} type="password" autoComplete="off" mono value={form.token} placeholder={saved.has_token ? '已设置，留空不修改' : '未设置'} disabled={disabled} onChange={(e) => edit({ token: e.target.value })} />
      <Input label="管理员群组 Chat ID" mono inputMode="numeric" value={form.chatId} placeholder="如 -1002231180042" error={errors.admin_chat_id} hint="目前只作测试消息的默认目标" disabled={disabled} onChange={(e) => edit({ chatId: e.target.value })} />
      <Input label="Bot 用户名" mono value={form.username} placeholder="@pandora_notify_bot" error={errors.bot_username} disabled={disabled} onChange={(e) => edit({ username: e.target.value })} />
      {dirty && <div className={`${css.span2} ${css.faint}`}>有未保存的修改；发送测试用的是已保存的配置，保存后再测。</div>}
    </ChannelCard>
  )
}
