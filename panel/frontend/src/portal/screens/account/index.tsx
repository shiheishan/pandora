import { useState } from 'react'
import { signOut, useRuntime } from '../../../shell/runtime'
import { QueryView } from '../../../ui'
import { Avatar, Btn, Row, Rows, SectionTitle } from '../../kit'
import { displayName, usePortalMe } from '../../queries'
import { formatDate } from '../common/traffic'
import css from './Account.module.css'
import { NotificationPrefsCard, TelegramCard } from './Connections'
import { DeviceList } from './DeviceList'
import { shortUserId } from './model'
import { PasswordSheet } from './PasswordSheet'
import { QuickLoginSheet, useQuickLogin } from './QuickLoginSheet'

export default function Account() {
  const runtime = useRuntime()
  const me = usePortalMe()
  const quick = useQuickLogin()

  const [pwOpen, setPwOpen] = useState(false)
  const [qlOpen, setQlOpen] = useState(false)

  return (
    <>
      <QueryView query={me} rows={1} isEmpty={() => false} empty={null}>
        {(m) => {
          const name = displayName(m) || m.email
          return (
            <div className={css.acct}>
              <Avatar name={name} size="md" solid />
              <div>
                <b>{m.email}</b>
                <small>
                  用户 ID{' '}
                  <span className={css.mono} title={m.user_id}>
                    {shortUserId(m.user_id)}
                  </span>{' '}
                  · 注册于 {formatDate(m.created_at)}
                </small>
              </div>
            </div>
          )
        }}
      </QueryView>

      <SectionTitle>登录</SectionTitle>
      <Rows>
        <Row
          icon="key"
          title="密码"
          sub="改密码后，其他设备会全部下线"
          value="修改"
          onClick={() => setPwOpen(true)}
        />
        <Row
          icon="link"
          title="在新设备上登录"
          sub="扫码或打开一次性链接，不用输密码"
          onClick={() => {
            setQlOpen(true)
            quick.generate()
          }}
        />
      </Rows>

      <SectionTitle>登录的设备</SectionTitle>
      <DeviceList onChangePassword={() => setPwOpen(true)} />

      <SectionTitle>Telegram</SectionTitle>
      <TelegramCard />

      <SectionTitle>通知方式</SectionTitle>
      <NotificationPrefsCard />

      <Btn
        tone="soft"
        full
        className={css.logout}
        onClick={() => void signOut(runtime)}
      >
        退出登录
      </Btn>

      <PasswordSheet open={pwOpen} onClose={() => setPwOpen(false)} />
      <QuickLoginSheet open={qlOpen} onClose={() => setQlOpen(false)} quick={quick} />
    </>
  )
}
