import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, Switch, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import css from './Composer.module.css'

const SETTINGS_KEY = ['admin', 'tickets', 'settings'] as const

const supportSchema = z.object({
  wait_for_reply: z.boolean(),
  auto_close_enabled: z.boolean(),
  auto_close_hours: z.number().int(),
  max_open_tickets: z.number().int(),
})

type Support = z.output<typeof supportSchema>

export function TicketSettingsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  // 关着时不挂载：每次打开都从已保存的值重新开始，不用在 effect 里重置草稿
  return open ? <TicketSettingsForm onClose={onClose} /> : null
}

function TicketSettingsForm({ onClose }: { onClose: () => void }) {
  const api = useApi()
  const client = useQueryClient()
  const toast = useToast()
  const fail = useFailure()
  const q = useQuery({
    queryKey: SETTINGS_KEY,
    queryFn: ({ signal }) => api.get('v1/settings/support', supportSchema, { signal }),
  })
  const [draft, setDraft] = useState<Support | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const save = useMutation({
    mutationFn: (body: Support) => api.post('v1/settings/support', supportSchema, { body }),
    onSuccess: () => {
      toast('工单设置已保存')
      void client.invalidateQueries({ queryKey: SETTINGS_KEY })
      onClose()
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const form = draft ?? q.data ?? null
  const submit = () => {
    if (!form) return
    const local: Record<string, string> = {}
    const h = form.auto_close_hours
    if (!Number.isInteger(h) || h < 1 || h > 720) local.auto_close_hours = '自动关闭时长要在 1 到 720 小时之间'
    const m = form.max_open_tickets
    if (!Number.isInteger(m) || m < 1 || m > 50) local.max_open_tickets = '同时打开的工单上限要在 1 到 50 之间'
    if (Object.keys(local).length) return setErrors(local)
    setErrors({})
    save.mutate(form)
  }

  return (
    <Modal
      open
      onClose={onClose}
      dismissible={!save.isPending}
      title="工单设置"
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={save.isPending}>取消</Button>
          <Button size="dialog" variant="primary" busy={save.isPending} disabled={!form} onClick={submit}>保存</Button>
        </>
      }
    >
      {!form ? (
        <p>加载中…</p>
      ) : (
        <div className={css.macroForm}>
          <Switch
            label="用户须等客服回复后才能继续发送"
            checked={form.wait_for_reply}
            disabled={save.isPending}
            onChange={(e) => setDraft({ ...form, wait_for_reply: e.target.checked })}
          />
          <Switch
            label="自动关闭"
            checked={form.auto_close_enabled}
            disabled={save.isPending}
            onChange={(e) => setDraft({ ...form, auto_close_enabled: e.target.checked })}
          />
          <Input
            label="自动关闭时长（小时）"
            hint="客服回复后用户超过这么久没有回复，工单自动关闭（每 10 分钟检查一次）；1 到 720"
            inputMode="numeric"
            value={String(form.auto_close_hours)}
            error={errors.auto_close_hours}
            disabled={save.isPending}
            onChange={(e) => {
              const n = Number.parseInt(e.target.value, 10)
              setDraft({ ...form, auto_close_hours: Number.isNaN(n) ? 0 : n })
            }}
          />
          <Input
            label="每人同时打开的工单上限"
            hint="用户同时未解决、未关闭的工单最多几张，到了上限要先处理完旧的才能再提交；1 到 50，缺省 5"
            inputMode="numeric"
            value={String(form.max_open_tickets)}
            error={errors.max_open_tickets}
            disabled={save.isPending}
            onChange={(e) => {
              const n = Number.parseInt(e.target.value, 10)
              setDraft({ ...form, max_open_tickets: Number.isNaN(n) ? 0 : n })
            }}
          />
        </div>
      )}
    </Modal>
  )
}
