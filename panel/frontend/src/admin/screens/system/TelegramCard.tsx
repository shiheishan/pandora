import { useMutation } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, QueryView, Switch, Tag, TextArea, useToast } from '../../../ui'
import {
  extrasBody,
  extrasFormFrom,
  parseChatId,
  telegramBody,
  telegramFormFrom,
  telegramStatus,
  type ExtrasForm,
  type TelegramForm,
} from './logic'
import { useFailure, useInvalidateSystem, useTelegramExtras, useTelegramSettings } from './queries'
import { telegramExtrasSchema, telegramSaved, telegramTested, type TelegramExtras, type TelegramSettings } from './schemas'
import css from './system.module.css'

/**
 * Telegram 渠道配置弹窗（一直挂载，由页面控制 open）：
 * 主设置与扩展设置两份数据、两份草稿，关弹窗不清草稿；保存时按各自是否改动分别提交。
 */
export function TelegramDialog({ open, onClose, onDirtyChange }: { open: boolean; onClose: () => void; onDirtyChange: (dirty: boolean) => void }) {
  const main = useTelegramSettings()
  const extras = useTelegramExtras()
  if (main.data && extras.data) return <TelegramDialogForm open={open} onClose={onClose} onDirtyChange={onDirtyChange} savedMain={main.data} savedExtras={extras.data} />
  // 两份数据没齐时组件挂在页面上不画任何东西；打开了才在弹窗里显示加载中 / 出错
  if (!open) return null
  return (
    <Modal open onClose={onClose} size={640} title="Telegram">
      {main.data ? (
        <QueryView query={extras} rows={4} empty={null}>
          {() => null}
        </QueryView>
      ) : (
        <QueryView query={main} rows={4} empty={null}>
          {() => null}
        </QueryView>
      )}
    </Modal>
  )
}

function TelegramDialogForm({
  open,
  onClose,
  onDirtyChange,
  savedMain,
  savedExtras,
}: {
  open: boolean
  onClose: () => void
  onDirtyChange: (dirty: boolean) => void
  savedMain: TelegramSettings
  savedExtras: TelegramExtras
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSystem()
  // null = 没改；关弹窗不清草稿
  const [mainDraft, setMainDraft] = useState<TelegramForm | null>(null)
  const [extrasDraft, setExtrasDraft] = useState<ExtrasForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [to, setTo] = useState('')

  const mainForm = mainDraft ?? telegramFormFrom(savedMain)
  const extrasForm = extrasDraft ?? extrasFormFrom(savedExtras)
  const mainDirty = mainDraft !== null && JSON.stringify(mainDraft) !== JSON.stringify(telegramFormFrom(savedMain))
  const extrasDirty = extrasDraft !== null && JSON.stringify(extrasDraft) !== JSON.stringify(extrasFormFrom(savedExtras))
  const dirty = mainDirty || extrasDirty

  useEffect(() => {
    onDirtyChange(dirty)
  }, [dirty, onDirtyChange])

  const editMain = (patch: Partial<TelegramForm>) => setMainDraft({ ...mainForm, ...patch })
  const editExtras = (patch: Partial<ExtrasForm>) => setExtrasDraft({ ...extrasForm, ...patch })

  const save = useMutation({
    mutationFn: async (payload: { main?: object; extras?: TelegramExtras }) => {
      let mainDone = false
      try {
        if (payload.main) {
          await api.post('v1/settings/telegram', telegramSaved, { body: payload.main })
          mainDone = true
        }
        if (payload.extras) await api.post('v1/settings/telegram/extras', telegramExtrasSchema, { body: payload.extras })
      } catch (e) {
        // 扩展失败时，已成功的主设置照常清草稿并失效，避免下次重复提交
        if (mainDone) {
          setMainDraft(null)
          void invalidate('telegram')
        }
        throw e
      }
    },
    onSuccess: () => {
      toast('Telegram 设置已保存')
      setMainDraft(null)
      setExtrasDraft(null)
      void invalidate('telegram')
      void invalidate('telegram-extras')
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const test = useMutation({
    mutationFn: (chat: number | null) => api.post('v1/settings/telegram/test', telegramTested, { body: chat === null ? {} : { chat_id: chat } }),
    onSuccess: () => toast('Telegram 测试消息已发送'),
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const submit = () => {
    const errs: Record<string, string> = {}
    let main: object | undefined
    if (mainDirty) {
      const body = telegramBody(mainForm, savedMain)
      if ('error' in body) errs.admin_chat_id = body.error
      else if (mainForm.enabled && !body.bot_username) errs.bot_username = '启用前要填 Bot 用户名，用户要靠它找到你的 bot'
      else main = body
    }
    let extras: TelegramExtras | undefined
    if (extrasDirty) {
      const body = extrasBody(extrasForm, savedExtras)
      if ('error' in body) Object.assign(errs, body.error)
      else extras = body
    }
    if (Object.keys(errs).length > 0) return setErrors(errs)
    setErrors({})
    save.mutate({ main, extras })
  }

  const sendTest = () => {
    const chat = parseChatId(to)
    if (!chat.ok) return setErrors({ chat_id: 'chat id 必须是非 0 整数' })
    if (chat.value === null && savedMain.admin_chat_id === null) return setErrors({ chat_id: '请填写要接收测试消息的 chat id，或先保存管理员群组 chat id' })
    setErrors({})
    test.mutate(chat.value)
  }

  const disabled = save.isPending
  const status = telegramStatus(savedMain)
  return (
    <Modal
      open={open}
      onClose={onClose}
      size={640}
      divided
      title="Telegram"
      headExtra={<Tag tone={status.tone === 'muted' ? 'neutral' : status.tone}>{status.label}</Tag>}
      footerStart={
        <>
          <Input
            size="sm"
            aria-label="测试 chat id"
            mono
            fieldClassName={css.grow}
            placeholder={savedMain.admin_chat_id === null ? '接收测试的 chat id' : '留空发送到管理员群组'}
            value={to}
            error={errors.chat_id}
            disabled={disabled}
            onChange={(e) => setTo(e.target.value)}
          />
          <Button size="sm" disabled={dirty} busy={test.isPending} title={dirty ? '测试用的是已保存的配置，请先保存' : undefined} onClick={sendTest}>
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
      <div className={css.formGrid}>
        <div className={css.span2}>
          <Switch label="启用 Telegram 通知" checked={mainForm.enabled} disabled={disabled} onChange={(e) => editMain({ enabled: e.target.checked })} />
        </div>
        <Input
          label="Bot Token"
          fieldClassName={css.span2}
          type="password"
          autoComplete="off"
          mono
          value={mainForm.token}
          placeholder={savedMain.has_token ? '已设置，留空不修改' : '未设置'}
          error={errors.bot_token}
          disabled={disabled}
          onChange={(e) => editMain({ token: e.target.value })}
        />
        <Input
          label="管理员群组 Chat ID"
          mono
          inputMode="numeric"
          value={mainForm.chatId}
          placeholder="如 -1002231180042"
          error={errors.admin_chat_id}
          hint="测试消息的默认目标；收款、工单提醒也发到这个群"
          disabled={disabled}
          onChange={(e) => editMain({ chatId: e.target.value })}
        />
        <Input
          label="Bot 用户名"
          mono
          value={mainForm.username}
          placeholder="@pandora_notify_bot"
          error={errors.bot_username}
          disabled={disabled}
          onChange={(e) => editMain({ username: e.target.value })}
        />
        <div className={`${css.span2} ${css.sectionHead}`}>机器人与群组</div>
        <div className={css.span2}>
          <Switch
            label="/sub 命令：在 Telegram 里发订阅链接"
            checked={extrasForm.subCommand}
            disabled={disabled}
            onChange={(e) => editExtras({ subCommand: e.target.checked })}
          />
          <div className={css.faint}>订阅链接等同于账号凭据，私聊里发出后谁拿到都能用；缺省关闭</div>
        </div>
        <div className={css.span2}>
          <TextArea
            label="/start 欢迎文案"
            placeholder="留空使用内置欢迎语"
            hint="用户直接发 /start（不带绑定码）时回复这段；最多 1000 字"
            rows={3}
            value={extrasForm.startMessage}
            error={errors.start_message}
            disabled={disabled}
            onChange={(e) => editExtras({ startMessage: e.target.value })}
          />
        </div>
        <div className={css.span2}>
          <Switch label="入群审核" checked={extrasForm.joinReview} disabled={disabled} onChange={(e) => editExtras({ joinReview: e.target.checked })} />
          <div className={css.faint}>用户申请加入下面的用户群时，已绑定账号、账号未停用且有有效订阅的自动通过，否则拒绝。群要开启「申请加入」，bot 要是群管理员并有邀请成员权限</div>
        </div>
        <Input
          label="用户群 Chat ID"
          mono
          inputMode="numeric"
          placeholder="如 -1001234567890"
          value={extrasForm.userGroupChatId}
          error={errors.user_group_chat_id}
          disabled={disabled}
          onChange={(e) => editExtras({ userGroupChatId: e.target.value })}
        />
        <Input
          label="讨论群链接"
          placeholder="https://t.me/…"
          hint="填了以后用户端「账号 → Telegram」显示这个入口"
          value={extrasForm.discussLink}
          error={errors.discuss_link}
          disabled={disabled}
          onChange={(e) => editExtras({ discussLink: e.target.value })}
        />
        {dirty && <div className={`${css.span2} ${css.warn}`}>有未保存的修改；发送测试用的是已保存的配置，保存后再测。</div>}
      </div>
    </Modal>
  )
}
