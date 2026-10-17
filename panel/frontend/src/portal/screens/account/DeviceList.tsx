import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { QueryView, Tag, useToast } from '../../../ui'
import { Btn, Icon, Row, Rows, Sheet } from '../../kit'
import css from './Account.module.css'
import { useRevokeSession, useSessions, type Session } from './api'
import { deviceKind, deviceName, lastSeenLabel, loginAtLabel, sortSessions } from './model'

export function DeviceList({ onChangePassword }: { onChangePassword: () => void }) {
  const sessions = useSessions()
  const [target, setTarget] = useState<Session | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)

  function askRevoke(s: Session) {
    setTarget(s)
    setConfirmOpen(true)
  }

  return (
    <>
      <QueryView query={sessions} rows={3} isEmpty={() => false} empty={null}>
        {(list) => {
          const sorted = sortSessions(list)
          const onlyCurrent = sorted.length <= 1

          return (
            <>
              <Rows>
                {sorted.map((s) => {
                  const name = deviceName(s.user_agent)
                  const icon = deviceKind(s.user_agent) === 'phone' ? 'phone' : 'desk'
                  const sub = `${lastSeenLabel(s.last_seen_at)} · 登录于 ${loginAtLabel(s.created_at)}`

                  if (s.current) {
                    return (
                      <Row
                        key={s.id}
                        icon={icon}
                        title={
                          <>
                            {name}
                            <Tag tone="ok" className={css.here}>
                              这台设备
                            </Tag>
                          </>
                        }
                        sub={sub}
                      />
                    )
                  }

                  return (
                    <Row
                      key={s.id}
                      icon={icon}
                      title={name}
                      sub={sub}
                      value={<span className={css.kick}>下线</span>}
                      chevron={false}
                      className={css.devRow}
                      onClick={() => askRevoke(s)}
                    />
                  )
                })}
              </Rows>

              <p className={css.cap}>
                {onlyCurrent ? (
                  '只有这台设备登录着。'
                ) : (
                  <>
                    看到不认识的设备，先让它下线，再
                    <button type="button" className={css.capLink} onClick={onChangePassword}>
                      改密码
                    </button>
                    （改完其他设备会全部下线）。
                  </>
                )}
              </p>
            </>
          )
        }}
      </QueryView>

      <RevokeConfirmSheet
        open={confirmOpen}
        session={target}
        onClose={() => setConfirmOpen(false)}
      />
    </>
  )
}

function RevokeConfirmSheet({
  open,
  session,
  onClose,
}: {
  open: boolean
  session: Session | null
  onClose: () => void
}) {
  const toast = useToast()
  const revoke = useRevokeSession()

  const name = session ? deviceName(session.user_agent) : ''
  const icon = session && deviceKind(session.user_agent) === 'phone' ? 'phone' : 'desk'
  const sub = session
    ? `${lastSeenLabel(session.last_seen_at)} · 登录于 ${loginAtLabel(session.created_at)}`
    : ''

  function go() {
    if (!session) return
    revoke.mutate(session.id, {
      onSuccess: () => {
        onClose()
        toast(`已将 ${name} 下线`)
      },
      onError: (e) => {
        onClose()
        if (isApiError(e, 'not_found')) {
          toast('这个会话已经下线')
        } else {
          toast(e.message || '操作失败，请稍后重试', 'danger')
        }
      },
    })
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="让这台设备下线？"
      actions={
        <>
          <Btn tone="soft" onClick={onClose}>
            取消
          </Btn>
          <Btn tone="danger" busy={revoke.isPending} onClick={go}>
            下线
          </Btn>
        </>
      }
    >
      <div className={css.devId}>
        <span className={css.devIcon}>
          <Icon name={icon} />
        </span>
        <span className={css.devMeta}>
          <b>{name}</b>
          <small>{sub}</small>
        </span>
      </div>

      <ul className={css.devFx}>
        <li>那台设备再打开这里，要重新登录</li>
        <li className={css.keep}>已经添加到 App 里的套餐照常能用，不用重新添加</li>
      </ul>
    </Sheet>
  )
}
