import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { z } from 'zod'
import { isApiError } from '../core/api'
import { useApi } from '../shell/runtime'
import { Button, ConfirmModal, Modal, QueryView, useToast } from '../ui'
import css from './TelegramBindDialog.module.css'

const TELEGRAM_KEY = ['admin', 'me', 'telegram'] as const
const POLL_MS = 3000
const BOUND_TEXT = 'Telegram 绑定成功'

const telegramSchema = z.object({
  bound: z.boolean(),
  username: z.string().optional(),
  bot_username: z.string().optional(),
  enabled: z.boolean(),
  discuss_link: z.string().nullable(),
})

const bindCodeSchema = z.object({ code: z.string().min(1), bot_username: z.string(), expires_at: z.string() })
type BindCode = z.output<typeof bindCodeSchema>

const secondsLeft = (deadline: number, now: number) => Math.max(0, Math.ceil((deadline - now) / 1000))
const formatCountdown = (seconds: number) => `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
const telegramDeepLink = (bot: string, code: string) => `https://t.me/${encodeURIComponent(bot)}?start=${encodeURIComponent(code)}`

function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [active])
  return now
}

export function TelegramBindDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  return open ? <TelegramBindForm onClose={onClose} /> : null
}

function TelegramBindForm({ onClose }: { onClose: () => void }) {
  const api = useApi()
  const client = useQueryClient()
  const toast = useToast()
  const telegram = useQuery({ queryKey: TELEGRAM_KEY, queryFn: ({ signal }) => api.get('v1/me/telegram', telegramSchema, { signal }) })
  const [code, setCode] = useState<BindCode | null>(null)
  const [asking, setAsking] = useState(false)
  const now = useNow(code !== null)
  const left = code ? secondsLeft(Date.parse(code.expires_at), now) : 0
  const waiting = code !== null && left > 0

  // refetch 是稳定引用；依赖整个查询对象的话，倒计时每秒重渲染会把轮询计时器不断重置
  const { refetch } = telegram
  useEffect(() => {
    if (!waiting) return
    const timer = setInterval(async () => {
      const r = await refetch()
      if (r.data?.bound) {
        setCode(null)
        toast(BOUND_TEXT)
      }
    }, POLL_MS)
    return () => clearInterval(timer)
  }, [waiting, refetch, toast])

  const bindCode = useMutation({ mutationFn: () => api.post('v1/me/telegram/bind-code', bindCodeSchema) })
  const unbind = useMutation({
    mutationFn: () => api.delete('v1/me/telegram', z.object({ unbound: z.literal(true) })),
    onSettled: () => void client.invalidateQueries({ queryKey: TELEGRAM_KEY }),
  })

  const issueCode = () => {
    bindCode.mutate(undefined, {
      onSuccess: (c) => setCode(c),
      onError: (e) => {
        toast(e.message || '获取失败，请稍后重试', 'danger')
        if (isApiError(e) && e.status >= 400 && e.status < 500) void telegram.refetch()
      },
    })
  }

  const checkBound = async () => {
    const r = await telegram.refetch()
    if (r.data?.bound) {
      setCode(null)
      toast(BOUND_TEXT)
    } else if (r.data) toast('还没有收到绑定消息，发送后稍等几秒再试')
  }

  return (
    <Modal open title="绑定 Telegram" onClose={onClose} dismissible={!bindCode.isPending && !unbind.isPending}>
      <QueryView query={telegram} rows={2} empty={null}>
        {(t) => {
          if (!t.enabled && !t.bound) {
            return <p className={css.muted}>站点未启用 Telegram 通知，先到「通知与插件 → 渠道」启用。</p>
          }
          if (t.bound) {
            return (
              <div className={css.stack}>
                <p className={css.muted}>
                  {t.username ? `已绑定 @${t.username}` : '已绑定 Telegram'}。在管理员群组里直接回复工单提醒消息，就会以你的身份回复工单。
                </p>
                <Button size="sm" onClick={() => setAsking(true)}>解绑</Button>
                <ConfirmModal
                  open={asking}
                  title="解绑 Telegram？"
                  body="解绑后在管理员群组里回复提醒将不再生效，之后可以重新绑定。"
                  confirmLabel="解绑"
                  tone="danger"
                  onCancel={() => setAsking(false)}
                  onConfirm={async () => {
                    try {
                      await unbind.mutateAsync()
                      toast('已解绑')
                    } catch (e) {
                      if (!isApiError(e, 'not_found')) toast(e instanceof Error && e.message ? e.message : '解绑失败，请稍后重试', 'danger')
                    }
                    setAsking(false)
                  }}
                />
              </div>
            )
          }
          if (code && left > 0) {
            return (
              <div className={css.stack}>
                <p className={css.muted}>向 @{code.bot_username} 发送绑定指令后，在管理员群组里回复工单提醒才会生效。</p>
                <div className={css.codeBox}>
                  <span className={css.hint}>发送：</span>
                  <code className={css.bindCode}>/start {code.code}</code>
                  <span className={css.hint}>{formatCountdown(left)} 内有效</span>
                </div>
                <div className={css.actions}>
                  <a className={css.linkButton} href={telegramDeepLink(code.bot_username, code.code)} target="_blank" rel="noopener noreferrer">
                    在 Telegram 中打开
                  </a>
                  <Button size="sm" onClick={() => void checkBound()}>我已发送，检查绑定状态</Button>
                </div>
              </div>
            )
          }
          return (
            <div className={css.stack}>
              <p className={css.muted}>绑定后可在管理员群组里直接回复工单提醒消息，以你的身份回复工单。</p>
              {code && left <= 0 && <p className={css.muted}>绑定码已过期，请重新获取</p>}
              <Button size="sm" busy={bindCode.isPending} onClick={issueCode}>{code ? '重新获取绑定码' : '获取绑定码'}</Button>
            </div>
          )
        }}
      </QueryView>
    </Modal>
  )
}
