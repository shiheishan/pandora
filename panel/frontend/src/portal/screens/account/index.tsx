/**
 * [INPUT]: 依赖 react 的 useState / FormEvent / ReactNode，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../shell/runtime 的 signOut / useRuntime，依赖 ../../../ui 的 Button / Card / Empty / Input / QueryView / Skeleton / Tag / useToast，依赖 ../../entry-links 的 quickLoginLink，依赖 ../../queries 的 usePortalMe，依赖 ../common/clients 的 copyText，依赖 ../common/traffic 的 formatDate，依赖 ./api、./clock、./model 与 ./Connections
 * [OUTPUT]: 默认导出 Account 页面组件（登记表 React.lazy 的目标）
 * [POS]: portal/screens/account 的入口：账号安全（门户-10）。两列卡片流（宽屏各占一列、窄屏一列）：左列个人信息、修改密码、快捷登录；右列登录会话、Telegram 与通知偏好（Connections.tsx）、退出登录。快捷登录只在这台已登录的设备上签发，链接 60 秒后自动隐藏（保留规则 1）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState, type FormEvent, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { signOut, useRuntime } from '../../../shell/runtime'
import { Button, Card, Empty, Input, QueryView, Tag, useToast } from '../../../ui'
import { quickLoginLink } from '../../entry-links'
import { usePortalMe } from '../../queries'
import { copyText } from '../common/clients'
import { formatDate } from '../common/traffic'
import css from './Account.module.css'
import { useChangePassword, useIssueQuickLogin, useRevokeSession, useSessions, type Session } from './api'
import { useNow } from './clock'
import { NotificationPrefsCard, TelegramCard } from './Connections'
import { deviceName, passwordErrors, secondsLeft, shortUserId, sortSessions, validateNewPassword, type PasswordErrors } from './model'

export default function Account() {
  const runtime = useRuntime()
  return (
    <div className={css.grid}>
      <div className={css.column}>
        <ProfileCard />
        <PasswordCard />
        <QuickLoginCard />
      </div>
      <div className={css.column}>
        <SessionsCard />
        <TelegramCard />
        <NotificationPrefsCard />
        <Button block className={css.logout} onClick={() => void signOut(runtime)}>
          退出登录
        </Button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 个人信息：GET v1/me；设计稿的「用户组」后端没有（契约门户-10 注），不显示
// ---------------------------------------------------------------------------
function ProfileCard() {
  const me = usePortalMe()
  return (
    <Card title="个人信息">
      <QueryView query={me} rows={3} isEmpty={() => false} empty={null}>
        {(m) => {
          const rows: Array<[string, ReactNode]> = [
            ['邮箱', m.email],
            [
              '用户 ID',
              <span className={css.mono} title={m.user_id}>
                {shortUserId(m.user_id)}
              </span>,
            ],
            ['注册时间', formatDate(m.created_at)],
          ]
          return (
            <dl className={css.facts}>
              {rows.map(([k, v]) => (
                <div key={k} className={css.fact}>
                  <dt>{k}</dt>
                  <dd>{v}</dd>
                </div>
              ))}
            </dl>
          )
        }}
      </QueryView>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 修改密码：当前密码错回 401（表单内联，不登出）；成功后其他会话下线、当前会话保留（修订 R62）
// ---------------------------------------------------------------------------
function PasswordCard() {
  const toast = useToast()
  const email = usePortalMe().data?.email ?? ''
  const change = useChangePassword()
  const [oldPw, setOldPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [errors, setErrors] = useState<PasswordErrors>({})

  function submit(event: FormEvent) {
    event.preventDefault()
    const local: PasswordErrors = {}
    if (!oldPw) local.old = '请输入当前密码'
    const rule = validateNewPassword(newPw)
    if (rule) local.next = rule
    else if (newPw === oldPw) local.next = '新密码不能与当前密码相同'
    setErrors(local)
    if (local.old || local.next) return
    change.mutate(
      { old_password: oldPw, new_password: newPw },
      {
        onSuccess: () => {
          setOldPw('')
          setNewPw('')
          toast('密码已更新，其他会话已下线')
        },
        onError: (e) => setErrors(passwordErrors(e)),
      },
    )
  }

  return (
    <Card title="修改密码">
      <form className={css.form} onSubmit={submit} noValidate>
        {/* 给密码管理器认账号用，不显示 */}
        <input type="email" name="username" autoComplete="username" value={email} readOnly hidden />
        <Input
          type="password"
          aria-label="当前密码"
          placeholder="当前密码"
          autoComplete="current-password"
          value={oldPw}
          onChange={(e) => setOldPw(e.target.value)}
          error={errors.old}
        />
        <Input
          type="password"
          aria-label="新密码"
          placeholder="新密码，至少 8 位，含字母和数字"
          autoComplete="new-password"
          value={newPw}
          onChange={(e) => setNewPw(e.target.value)}
          error={errors.next}
        />
        {errors.form && (
          <div className={css.formError} role="alert">
            {errors.form}
          </div>
        )}
        <Button type="submit" variant="primary" block busy={change.isPending}>
          更新密码
        </Button>
      </form>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 快捷登录（保留规则 1）：只在已登录的这台设备生成，60 秒内在新设备打开，一次性；
// 到时自动隐藏链接，按钮变回「生成快捷登录链接」。倒数以 expires_in 为准，不受本机时钟偏差影响
// ---------------------------------------------------------------------------
function QuickLoginCard() {
  const toast = useToast()
  const issue = useIssueQuickLogin()
  const [link, setLink] = useState<{ url: string; deadline: number } | null>(null)
  const now = useNow(link !== null)
  const left = link ? secondsLeft(link.deadline, now) : 0

  function generate() {
    issue.mutate(undefined, {
      onSuccess: async (out) => {
        const created = { url: quickLoginLink(out.token), deadline: Date.now() + out.expires_in * 1000 }
        setLink(created)
        // 到点收起；期间重新生成过就不动新的那条
        setTimeout(() => setLink((cur) => (cur === created ? null : cur)), out.expires_in * 1000)
        toast((await copyText(created.url)) ? '链接已生成并复制' : '链接已生成，请手动复制')
      },
      onError: (e) => toast(e.message || '生成失败，请稍后重试', 'danger'),
    })
  }

  const shown = link && left > 0 ? link : null
  return (
    <Card title="快捷登录">
      <p className={css.muted}>生成一次性登录链接，在新设备上打开即可登录。链接 60 秒内有效，仅可使用一次。</p>
      {shown && (
        <div className={css.quick}>
          <code className={css.code}>{shown.url}</code>
          <div className={css.quickBar}>
            <span className={css.countdown}>{left} 秒后失效</span>
            <Button
              size="sm"
              onClick={async () => {
                const ok = await copyText(shown.url)
                toast(ok ? '已复制' : '复制失败，请手动选择链接复制', ok ? undefined : 'danger')
              }}
            >
              复制
            </Button>
          </div>
        </div>
      )}
      <Button block busy={issue.isPending} onClick={generate}>
        {shown ? '重新生成' : '生成快捷登录链接'}
      </Button>
    </Card>
  )
}

// ---------------------------------------------------------------------------
// 登录会话：只列门户会话（修订 R15）；位置与 IP 按 D-F-3（已决）不显示、最近活跃未实现（修订 R62），
// meta 只写登录时间；当前会话标「当前」、不能下线
// ---------------------------------------------------------------------------
function SessionsCard() {
  const sessions = useSessions()
  return (
    <Card flush title="登录会话">
      <QueryView query={sessions} rows={3} empty={<Empty bare title="没有其他登录会话" description="在别的设备登录后会出现在这里。" />}>
        {(list) => (
          <ul className={css.sessions}>
            {sortSessions(list).map((s) => (
              <SessionRow key={s.id} session={s} />
            ))}
          </ul>
        )}
      </QueryView>
    </Card>
  )
}

function SessionRow({ session }: { session: Session }) {
  const toast = useToast()
  const revoke = useRevokeSession()
  const name = deviceName(session.user_agent)
  return (
    <li className={css.session}>
      <div className={css.sessionMain}>
        <div className={css.sessionName}>
          {name}
          {session.current && <Tag tone="ok">当前</Tag>}
        </div>
        <div className={css.sessionMeta}>登录于 {formatDateTime(session.created_at)}</div>
      </div>
      {!session.current && (
        <Button
          size="sm"
          className={css.kick}
          busy={revoke.isPending}
          aria-label={`将 ${name} 下线`}
          onClick={() =>
            revoke.mutate(session.id, {
              onSuccess: () => toast(`已将 ${name} 下线`),
              // 404：已下线或已过期，列表随即重拉
              onError: (e) => toast(isApiError(e, 'not_found') ? '这个会话已经下线' : e.message || '操作失败，请稍后重试', isApiError(e, 'not_found') ? undefined : 'danger'),
            })
          }
        >
          下线
        </Button>
      )}
    </li>
  )
}
