/**
 * [INPUT]: 依赖 react 的 useEffect / useState，依赖 zod 的类型推导，依赖 ../../../core/api 的 isApiError，依赖 ../../../ui 的 Button / Card / ConfirmModal / QueryView / Switch / Tag / useToast，依赖 ./api 的 Telegram 与通知偏好读写，依赖 ./clock 的 useNow，依赖 ./model 的偏好行与倒计时
 * [OUTPUT]: 对外提供 TelegramCard、NotificationPrefsCard
 * [POS]: portal/screens/account 的外部通知渠道两张卡（契约门户-10）：Telegram 卡按「站点未启用 / 已绑定 / 展示绑定码 / 未绑定」四态，绑定码指令是 /start CODE（不是设计稿的 /bind）、10 分钟倒计时、展示期间 3 秒轮询绑定状态并给 t.me 深链；通知偏好三行 × 邮件 / Telegram 两列，交易类锁定开启，Telegram 列在未绑定或站点未启用时置灰。两卡共用 ['portal','account','telegram'] 一个查询
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import type { z } from 'zod'
import { isApiError } from '../../../core/api'
import { Button, Card, ConfirmModal, QueryView, Switch, Tag, useToast } from '../../../ui'
import css from './Account.module.css'
import { useBindCode, usePreferences, useSetPreference, useTelegram, useUnbindTelegram, type bindCodeSchema, type Telegram } from './api'
import { useNow } from './clock'
import { formatCountdown, PREF_CHANNELS, PREF_ROWS, secondsLeft, telegramDeepLink, type PrefChannel } from './model'

type BindCode = z.output<typeof bindCodeSchema>

const POLL_MS = 3000
const BOUND_TEXT = 'Telegram 绑定成功'

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------
export function TelegramCard() {
  const toast = useToast()
  const [code, setCode] = useState<BindCode | null>(null)
  const now = useNow(code !== null)
  const left = code ? secondsLeft(Date.parse(code.expires_at), now) : 0
  const waiting = code !== null && left > 0
  const telegram = useTelegram()
  const bound = telegram.data?.bound === true
  const { refetch } = telegram

  // 绑定成功没有实时事件：展示绑定码期间 3 秒重拉一次，绑上了就收起绑定码
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
  const bindDone = () => {
    setCode(null)
    toast(BOUND_TEXT)
  }

  const status = !telegram.data ? null : !telegram.data.enabled && !bound ? '未启用' : bound ? '已绑定' : '未绑定'
  return (
    <Card title="Telegram" extra={status && (bound ? <Tag tone="ok">{status}</Tag> : <span className={css.status}>{status}</span>)}>
      <QueryView query={telegram} rows={1} isEmpty={() => false} empty={null}>
        {(t) => (t.bound ? <Bound info={t} /> : !t.enabled ? <p className={css.muted}>站点未启用 Telegram 通知。</p> : <Unbound code={code} left={left} onCode={setCode} onBound={bindDone} />)}
      </QueryView>
    </Card>
  )
}

function Bound({ info }: { info: Telegram }) {
  const toast = useToast()
  const unbind = useUnbindTelegram()
  const [asking, setAsking] = useState(false)
  return (
    <>
      <div className={css.tgRow}>
        <span className={css.grow}>{info.username ? `已绑定 @${info.username}` : '已绑定 Telegram'}，到期提醒和工单回复将推送到 Telegram</span>
        <Button size="sm" onClick={() => setAsking(true)}>
          解绑
        </Button>
      </div>
      {!info.enabled && <p className={css.muted}>站点目前暂停了 Telegram 通知，恢复前不会推送。</p>}
      <ConfirmModal
        open={asking}
        title="解绑 Telegram？"
        body="将不再收到 Telegram 推送，之后可以重新绑定。"
        confirmLabel="解绑"
        tone="danger"
        onCancel={() => setAsking(false)}
        onConfirm={async () => {
          try {
            await unbind.mutateAsync()
            toast('已解绑')
          } catch (e) {
            // 404：别处已经解绑，列表随 onSettled 重拉
            if (!isApiError(e, 'not_found')) toast(e instanceof Error && e.message ? e.message : '解绑失败，请稍后重试', 'danger')
          }
          setAsking(false)
        }}
      />
    </>
  )
}

function Unbound({ code, left, onCode, onBound }: { code: BindCode | null; left: number; onCode: (c: BindCode | null) => void; onBound: () => void }) {
  const toast = useToast()
  const issue = useBindCode()
  const telegram = useTelegram()
  const [checking, setChecking] = useState(false)

  function getCode() {
    issue.mutate(undefined, {
      onSuccess: (c) => onCode(c),
      onError: (e) => {
        toast(e.message || '获取失败，请稍后重试', 'danger')
        // 422 站点未启用、409 已绑定：状态变了，重拉
        if (isApiError(e) && e.status >= 400 && e.status < 500) void telegram.refetch()
      },
    })
  }

  async function check() {
    setChecking(true)
    const r = await telegram.refetch()
    setChecking(false)
    if (r.data?.bound) onBound()
    else if (r.data) toast('还没有收到绑定消息，发送后稍等几秒再试')
  }

  if (code && left > 0) {
    return (
      <>
        <div className={css.codeBox}>
          <span className={css.muted}>向 @{code.bot_username} 发送：</span>
          <code className={css.bindCode}>/start {code.code}</code>
          <span className={css.hint}>{formatCountdown(left)} 内有效</span>
        </div>
        <div className={css.actions}>
          <a className={css.linkButton} href={telegramDeepLink(code.bot_username, code.code)} target="_blank" rel="noopener noreferrer">
            在 Telegram 中打开
          </a>
          <Button variant="outline" busy={checking} onClick={() => void check()}>
            我已发送，检查绑定状态
          </Button>
        </div>
      </>
    )
  }
  return (
    <>
      {code && <p className={css.muted}>绑定码已过期，请重新获取。</p>}
      <Button block busy={issue.isPending} onClick={getCode}>
        获取绑定码
      </Button>
    </>
  )
}

// ---------------------------------------------------------------------------
// 通知偏好：每点一次改一项，乐观更新、失败回滚
// ---------------------------------------------------------------------------
const CHANNEL_LABEL: Record<PrefChannel, string> = { email: '邮件', telegram: 'Telegram' }

export function NotificationPrefsCard() {
  const toast = useToast()
  const prefs = usePreferences()
  const telegram = useTelegram()
  const set = useSetPreference()
  const tg = telegram.data
  const tgUsable = tg?.enabled === true && tg.bound

  return (
    <Card flush>
      <div className={css.prefHead} role="presentation">
        <h3 className={css.prefTitle}>通知偏好</h3>
        {PREF_CHANNELS.map((c) => (
          <span key={c} className={css.prefCol}>
            {CHANNEL_LABEL[c]}
          </span>
        ))}
      </div>
      <QueryView query={prefs} rows={3} isEmpty={(d) => d.length === 0} empty={null}>
        {(list) => (
          <>
            {PREF_ROWS.map((row) => (
              <div key={row.category} className={css.prefRow}>
                <span className={css.prefLabel}>
                  {row.label}
                  <span className={css.hint}>{row.hint}</span>
                </span>
                {PREF_CHANNELS.map((channel) => {
                  const p = list.find((x) => x.category === row.category && x.channel === channel)
                  const usable = channel === 'email' || tgUsable
                  return (
                    <span key={channel} className={css.prefCol}>
                      {p && (
                        <Switch
                          aria-label={`${row.label} · ${CHANNEL_LABEL[channel]}`}
                          checked={p.enabled && usable}
                          disabled={p.locked || !usable}
                          onChange={(e) =>
                            set.mutate({ category: row.category, channel, enabled: e.target.checked }, { onError: (err) => toast(err.message || '保存失败，已恢复原设置', 'danger') })
                          }
                        />
                      )}
                    </span>
                  )
                })}
              </div>
            ))}
            {tg && !tgUsable && <p className={css.prefNote}>{tg.enabled ? '绑定 Telegram 后可开启 Telegram 推送。' : '站点未启用 Telegram 通知，Telegram 一列不可用。'}</p>}
          </>
        )}
      </QueryView>
    </Card>
  )
}
