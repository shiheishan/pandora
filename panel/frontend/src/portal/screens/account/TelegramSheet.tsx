import { useEffect, useState } from 'react'
import type { z } from 'zod'
import { isApiError } from '../../../core/api'
import { Skeleton, useToast } from '../../../ui'
import { Btn, Icon, LinkChip, Sheet, cx } from '../../kit'
import { copyText } from '../common/clients'
import { QrCode } from '../common/qr'
import { bindCodeSchema, useBindCode, useTelegram } from './api'
import { useNow } from './clock'
import css from './Connections.module.css'
import { bindDeadline, formatCountdown, secondsLeft, telegramDeepLink } from './model'

type BindCode = z.output<typeof bindCodeSchema>

const POLL_MS = 3000

export interface TelegramBind {
  code: (BindCode & { deadline: number }) | null
  failed: string | null
  pending: boolean
  request: () => void
}

export function useTelegramBind(): TelegramBind {
  const telegram = useTelegram()
  const issue = useBindCode()
  const [code, setCode] = useState<(BindCode & { deadline: number }) | null>(null)
  const [failed, setFailed] = useState<string | null>(null)

  const request = () => {
    issue.mutate(undefined, {
      onSuccess: (c) => {
        setCode({ ...c, deadline: bindDeadline(c.expires_at, Date.now()) })
        setFailed(null)
      },
      onError: (e) => {
        setCode(null)
        setFailed(e.message || '获取失败，请稍后重试')
        if (isApiError(e) && e.status >= 400 && e.status < 500) {
          void telegram.refetch()
        }
      },
    })
  }

  return {
    code,
    failed,
    pending: issue.isPending,
    request,
  }
}

export function TelegramSheet({
  open,
  onClose,
  bind,
}: {
  open: boolean
  onClose: () => void
  bind: TelegramBind
}) {
  const toast = useToast()
  const telegram = useTelegram()
  const { refetch } = telegram
  const [copied, setCopied] = useState(false)

  const done = open && telegram.data?.bound === true
  const { code, failed, pending, request } = bind

  const now = useNow(open && code !== null && !done)
  const left = code ? secondsLeft(code.deadline, now) : 0
  const gone = code !== null && left <= 0

  // 绑定成功没有实时事件：弹层开着、码没过期时每 3 秒重拉一次绑定状态，绑上了 done 自动为真
  // （依赖只放稳定的 refetch：倒数每秒重渲染，放整个查询对象会让定时器每秒重建、永远等不到 3 秒）
  useEffect(() => {
    if (!open || code === null || gone || done) return
    const timer = setInterval(() => void refetch(), POLL_MS)
    return () => clearInterval(timer)
  }, [open, code, gone, done, refetch])

  const copyCode = async () => {
    if (!code) return
    const ok = await copyText(`/start ${code.code}`)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } else {
      toast('复制失败，请手动输入', 'danger')
    }
  }

  return (
    <Sheet open={open} onClose={onClose} title="绑定 Telegram" size={560}>
      {done ? (
        <div className={css.done}>
          <div className={css.doneIcon}>
            <Icon name="check" size={28} />
          </div>
          <b>{telegram.data?.username ? `已绑定 @${telegram.data.username}` : '已绑定 Telegram'}</b>
          <p>到期、流量和工单回复的提醒会发到这个 Telegram。哪些要发，在下面的「通知方式」里选。</p>
          <div className={css.doneBtn}>
            <Btn full onClick={onClose}>
              完成
            </Btn>
          </div>
        </div>
      ) : (
        <div>
          <div className={css.flow}>
            <ol className={css.how}>
              <li>
                <span className={css.desk}>
                  点<b>打开 Telegram</b>。Telegram 装在手机上的话，用手机相机扫右边的码。
                </span>
                <span className={css.mobile}>
                  点下面的<b>打开 Telegram</b>。
                </span>
              </li>
              <li>
                <span>
                  在和{' '}
                  {code ? <b>@{code.bot_username}</b> : <Skeleton width={120} height={14} />} 的对话里点
                  <b>开始</b>。
                </span>
              </li>
              <li>
                <span>回到这里，会自动显示绑定好了。</span>
              </li>
            </ol>
            <div className={css.qrBox}>
              {code ? (
                <QrCode
                  value={telegramDeepLink(code.bot_username, code.code)}
                  label="打开 Telegram 机器人的二维码"
                  className={css.qr}
                />
              ) : (
                <Skeleton className={css.qrSkeleton} />
              )}
              {gone && (
                <div className={css.qrOver}>
                  <b>已过期</b>
                  <Btn size="sm" busy={pending} onClick={request}>
                    重新获取
                  </Btn>
                </div>
              )}
            </div>
          </div>

          <div className={css.mainBtn}>
            {gone || !code ? (
              <Btn full busy={pending} onClick={request}>
                重新获取
              </Btn>
            ) : (
              <Btn
                full
                href={telegramDeepLink(code.bot_username, code.code)}
                target="_blank"
                rel="noopener noreferrer"
              >
                打开 Telegram
              </Btn>
            )}
          </div>

          <div
            className={cx(css.wait, (gone || (!code && failed)) && css.lapsed)}
            role="status"
          >
            {failed && !code ? (
              <span>{failed}</span>
            ) : gone ? (
              <span>绑定码已过期（10 分钟有效）</span>
            ) : (
              <>
                <i className={css.spin} aria-hidden="true" />
                <span>正在等你在 Telegram 里点开始 · {formatCountdown(left)} 后过期</span>
              </>
            )}
          </div>

          {code && (
            <details className={css.manual}>
              <summary>打不开 Telegram？手动发送</summary>
              <p>
                在 Telegram 里搜 <b>@{code.bot_username}</b>，把下面这行发给它：
              </p>
              <LinkChip
                className={css.manualChip}
                code={`/start ${code.code}`}
                action={
                  <button type="button" onClick={() => void copyCode()}>
                    {copied ? '已复制' : '复制'}
                  </button>
                }
              />
            </details>
          )}
        </div>
      )}
    </Sheet>
  )
}
