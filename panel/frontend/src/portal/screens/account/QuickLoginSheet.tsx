import { useState } from 'react'
import { Skeleton, useToast } from '../../../ui'
import { quickLoginLink } from '../../entry-links'
import { Btn, LinkChip, Sheet, cx } from '../../kit'
import { copyText } from '../common/clients'
import { QrCode } from '../common/qr'
import css from './Account.module.css'
import { useIssueQuickLogin } from './api'
import { useNow } from './clock'
import { formatCountdown, secondsLeft } from './model'

export interface QuickLogin {
  link: { url: string; deadline: number; total: number } | null
  failed: string | null
  pending: boolean
  generate: () => void
}

export function useQuickLogin(): QuickLogin {
  const issue = useIssueQuickLogin()
  const [link, setLink] = useState<{ url: string; deadline: number; total: number } | null>(null)
  const [failed, setFailed] = useState<string | null>(null)

  function generate() {
    // 新令牌发出时服务端作废旧的：先撤掉上一条，别让看着有效的旧码露出来
    setLink(null)
    issue.mutate(undefined, {
      onSuccess: (out) => {
        setLink({
          url: quickLoginLink(out.token),
          deadline: Date.now() + out.expires_in * 1000,
          total: out.expires_in,
        })
        setFailed(null)
      },
      onError: (e) => {
        setLink(null)
        setFailed(e.message || '生成失败，请稍后重试')
      },
    })
  }

  return {
    link,
    failed,
    pending: issue.isPending,
    generate,
  }
}

export function QuickLoginSheet({
  open,
  onClose,
  quick,
}: {
  open: boolean
  onClose: () => void
  quick: QuickLogin
}) {
  const toast = useToast()
  const [copied, setCopied] = useState(false)
  const { link, failed, pending, generate } = quick

  const now = useNow(open && link !== null)
  const left = link ? secondsLeft(link.deadline, now) : 0
  const expired = link !== null && left <= 0

  async function copy() {
    if (!link) return
    const ok = await copyText(link.url)
    if (ok) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } else {
      toast('复制失败，请手动选择链接复制', 'danger')
    }
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="在新设备上登录"
      actions={
        <Btn tone="soft" onClick={onClose}>
          完成
        </Btn>
      }
    >
      <p className={css.sheetLead}>用新设备的相机扫这个码，或者把链接发到新设备上打开，就能直接登录。</p>

      <div className={css.qrBox}>
        {link && <QrCode value={link.url} label="登录二维码" className={css.qr} />}
        {!link && pending && <Skeleton className={css.qrSkeleton} />}
        {!link && !pending && failed && (
          <div className={css.qrCenter}>
            <span>{failed}</span>
            <Btn size="sm" onClick={generate}>
              重试
            </Btn>
          </div>
        )}
        {expired && (
          <div className={css.qrOver}>
            <b>已失效</b>
            <Btn size="sm" busy={pending} onClick={generate}>
              重新生成
            </Btn>
          </div>
        )}
      </div>

      {link && (
        <div className={cx(css.qlTime, left <= 10 && left > 0 && css.low, expired && css.lapsed)}>
          <span>{expired ? '已失效，重新生成一个' : `${formatCountdown(left)} 后失效 · 只能用一次`}</span>
          <span className={css.qlTrack} aria-hidden="true">
            <span className={css.qlBar} style={{ width: `${link.total > 0 ? (left / link.total) * 100 : 0}%` }} />
          </span>
        </div>
      )}

      {link && (
        <LinkChip
          className={cx(css.qlLink, expired && css.off)}
          code={link.url}
          action={
            <button type="button" disabled={expired} onClick={() => void copy()}>
              {copied ? '已复制' : '复制'}
            </button>
          }
        />
      )}

      <p className={css.qlWarn}>只发给自己的设备：拿到这个链接的人能直接登录你的账号。</p>
    </Sheet>
  )
}
