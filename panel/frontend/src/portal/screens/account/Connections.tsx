import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { ConfirmModal, QueryView, Switch, useToast } from '../../../ui'
import { Btn, Icon } from '../../kit'
import { usePreferences, useSetPreference, useTelegram, useUnbindTelegram } from './api'
import css from './Connections.module.css'
import { PREF_CHANNELS, PREF_ROWS, type PrefChannel } from './model'
import { TelegramSheet, useTelegramBind } from './TelegramSheet'

function discussLinkHref(link: string | null | undefined): string | null {
  if (link && link.startsWith('https://')) return link
  return null
}

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------
export function TelegramCard() {
  const toast = useToast()
  const telegram = useTelegram()
  const unbind = useUnbindTelegram()
  const bind = useTelegramBind()

  const [open, setOpen] = useState(false)
  const [asking, setAsking] = useState(false)

  const t = telegram.data
  const bound = t?.bound === true
  const enabled = t?.enabled === true
  const username = t?.username
  const discussHref = discussLinkHref(t?.discuss_link)

  return (
    <div className={css.card}>
      <QueryView query={telegram} rows={1} isEmpty={() => false} empty={null}>
        {() => (
          <>
            <div className={css.head}>
              <span className={css.ri}>
                <Icon name="send" />
              </span>
              <span className={css.text}>
                Telegram 提醒
                <small>
                  {bound ? (
                    username ? (
                      <>
                        已绑定 <b>@{username}</b> · 提醒会发到这个账号
                      </>
                    ) : (
                      '已绑定 Telegram · 提醒会发到这个账号'
                    )
                  ) : enabled ? (
                    '绑定后，到期、流量和工单回复的提醒也能发到 Telegram'
                  ) : (
                    '站点未启用 Telegram 通知'
                  )}
                </small>
                {bound && !enabled && (
                  <small>站点目前暂停了 Telegram 通知，恢复前不会推送。</small>
                )}
              </span>

              {bound ? (
                <button
                  type="button"
                  className={css.unlink}
                  onClick={() => setAsking(true)}
                >
                  解除绑定
                </button>
              ) : enabled ? (
                <Btn
                  tone="soft"
                  size="sm"
                  busy={bind.pending && !open}
                  onClick={() => {
                    setOpen(true)
                    bind.request()
                  }}
                >
                  绑定
                </Btn>
              ) : null}
            </div>

            {discussHref && (
              <div className={css.group}>
                <a href={discussHref} target="_blank" rel="noopener noreferrer">
                  加入用户讨论群 ↗
                </a>
                <span>入群申请可能需要先绑定 Telegram 并有在用的套餐</span>
              </div>
            )}
          </>
        )}
      </QueryView>

      <TelegramSheet open={open} onClose={() => setOpen(false)} bind={bind} />

      <ConfirmModal
        open={asking}
        title="解除绑定 Telegram？"
        body="将不再收到 Telegram 推送，之后可以重新绑定。"
        confirmLabel="解除绑定"
        cancelLabel="再想想"
        tone="danger"
        onCancel={() => setAsking(false)}
        onConfirm={async () => {
          try {
            await unbind.mutateAsync()
            toast('已解除绑定')
          } catch (e) {
            if (!isApiError(e, 'not_found')) {
              toast(e instanceof Error && e.message ? e.message : '解除绑定失败，请稍后重试', 'danger')
            }
          }
          setAsking(false)
        }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// 通知方式：3 类 × 2 渠道，营销类没有 Telegram 一格
// ---------------------------------------------------------------------------
const CHANNEL_LABEL: Record<PrefChannel, string> = { email: '邮件', telegram: 'Telegram' }

export function NotificationPrefsCard() {
  const toast = useToast()
  const prefs = usePreferences()
  const telegram = useTelegram()
  const set = useSetPreference()
  const tg = telegram.data
  const tgUsable = tg?.enabled === true && tg.bound === true

  return (
    <div className={css.pref}>
      <div className={`${css.pf} ${css.hd}`} role="presentation">
        <span>通知</span>
        <span>邮件</span>
        <span>Telegram</span>
      </div>

      <QueryView query={prefs} rows={3} isEmpty={(d) => d.length === 0} empty={null}>
        {(list) => (
          <>
            {PREF_ROWS.map((row) => (
              <div key={row.category} className={css.pf}>
                <span>
                  <b>{row.label}</b>
                  <small>{row.hint}</small>
                </span>
                {PREF_CHANNELS.map((channel) => {
                  if (!row.channels.includes(channel)) {
                    return <span key={channel} className={css.pfCell} />
                  }
                  const p = list.find((x) => x.category === row.category && x.channel === channel)
                  const usable = channel === 'email' || tgUsable
                  return (
                    <span key={channel} className={css.pfCell}>
                      {p && (
                        <Switch
                          aria-label={`${row.label} · ${CHANNEL_LABEL[channel]}`}
                          checked={p.enabled && usable}
                          disabled={p.locked || !usable}
                          onChange={(e) =>
                            set.mutate(
                              { category: row.category, channel, enabled: e.target.checked },
                              {
                                onError: (err) =>
                                  toast(err.message || '保存失败，已恢复原设置', 'danger'),
                              },
                            )
                          }
                        />
                      )}
                    </span>
                  )
                })}
              </div>
            ))}

            {tg && !tgUsable && (
              <div className={css.note}>
                {tg.enabled
                  ? '绑定 Telegram 后可以开启 Telegram 推送。'
                  : '站点未启用 Telegram 通知，Telegram 一列不能开。'}
              </div>
            )}
          </>
        )}
      </QueryView>
    </div>
  )
}
