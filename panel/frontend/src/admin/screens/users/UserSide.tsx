import { useState } from 'react'
import { formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Select, Tag, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { SectionTitle, SideFact, SideFacts } from '../../kit'
import { okSchema, useInvalidateUsers, useUserGroups, useUserProfile, type UserDetail } from './api'
import css from './Detail.module.css'
import { ResetPasswordDialog } from './dialogs'
import { EmailDialog, NoteDialog, ReferrerDialog } from './UserEditDialogs'
import { assignableGroups, groupOptionLabel, LOGIN_IPS_WARN, loginIps7d } from './model'

type Dialog = 'email' | 'note' | 'referrer' | 'password' | null

export function UserSide({ d, now, onAdjustBalance }: { d: UserDetail; now: Date; onAdjustBalance: () => void }) {
  const invalidate = useInvalidateUsers()
  const profile = useUserProfile(d.id)
  const [dialog, setDialog] = useState<Dialog>(null)
  const [groupEdit, setGroupEdit] = useState(false)
  const deleted = d.status === 'deleted'
  const ipCount = profile.data ? loginIps7d(profile.data.ips, now) : null
  const ipWarn = ipCount !== null && ipCount >= LOGIN_IPS_WARN

  const linkBtn = (label: string, onClick: () => void) => (
    <Button size="xs" variant="link" onClick={onClick}>{label}</Button>
  )

  return (
    <>
      <SectionTitle>详情</SectionTitle>
      <SideFacts label="用户资料">
        <SideFact
          label="邮箱"
          action={!deleted ? linkBtn('修改', () => setDialog('email')) : undefined}
        >
          {d.email}
          {d.email_verified ? <Tag tone="ok">已验证</Tag> : <Tag tone="warn">未验证</Tag>}
        </SideFact>
        <SideFact label="Telegram">
          {/* 用户没设 Telegram 用户名时后端给空串：不写孤零零的「@」 */}
          {!d.telegram ? (
            <span className={css.muted}>未绑定</span>
          ) : d.telegram.username.trim() ? (
            `@${d.telegram.username.trim()}`
          ) : (
            '已绑定（无用户名）'
          )}
        </SideFact>
        <SideFact label="最近登录">
          {!d.last_login_at ? (
            '从未登录'
          ) : (
            <>
              {relativeTime(d.last_login_at, now)}
              {profile.isSuccess && (
                <>
                  {' · 7 天内 '}
                  <span className={ipWarn ? css.warn : undefined}>{ipCount} 个 IP</span>
                </>
              )}
            </>
          )}
        </SideFact>
        <SideFact
          label="用户组"
          action={
            !deleted
              ? groupEdit
                ? linkBtn('取消', () => setGroupEdit(false))
                : linkBtn('调整', () => setGroupEdit(true))
              : undefined
          }
        >
          {groupEdit ? (
            <GroupPicker d={d} onDone={() => setGroupEdit(false)} />
          ) : (
            d.group_name || '默认'
          )}
        </SideFact>
        <SideFact label="余额" action={!deleted ? linkBtn('调整', onAdjustBalance) : undefined}>
          <span className={css.mono}>{formatMoney(d.balance)}</span>
        </SideFact>
        <SideFact label="注册时间">
          <span className={css.mono}>{formatDateTime(d.created_at).slice(0, 10)}</span>
        </SideFact>
        <SideFact
          label="邀请人"
          action={!deleted ? linkBtn('修改', () => setDialog('referrer')) : undefined}
        >
          {d.referrer ? (
            <a className={css.link} href={href(`/users/list/${encodeURIComponent(d.referrer.id)}`)}>{d.referrer.email}</a>
          ) : (
            <span className={css.muted}>无</span>
          )}
        </SideFact>
        <SideFact label="显示名与备注" action={!deleted ? linkBtn('修改', () => setDialog('note')) : undefined}>
          {d.display_name || '—'}
        </SideFact>
        <SideFact label="登录密码" action={!deleted ? linkBtn('重置', () => setDialog('password')) : undefined}>
          已设置
        </SideFact>
        <SideFact label="身份">{d.is_admin ? '管理员' : '普通用户'}</SideFact>
      </SideFacts>

      <EmailDialog user={d} open={dialog === 'email'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <NoteDialog user={d} open={dialog === 'note'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <ReferrerDialog user={d} open={dialog === 'referrer'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <ResetPasswordDialog user={d} open={dialog === 'password'} onClose={() => setDialog(null)} />
    </>
  )
}

/** 用户组：POST v1/users/{id}/group（无幂等）；「未分组（默认）」传空串（契约） */
function GroupPicker({ d, onDone }: { d: UserDetail; onDone: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateUsers()
  const groups = useUserGroups()
  const [busy, setBusy] = useState(false)
  const options = assignableGroups(groups.data ?? [], d.group_id ? [d.group_id] : []).map((g) => ({ value: g.id, label: groupOptionLabel(g) }))
  const assign = async (groupId: string) => {
    setBusy(true)
    try {
      await api.post(`v1/users/${encodeURIComponent(d.id)}/group`, okSchema, { body: { group_id: groupId } })
      toast(groupId ? `已分配到「${options.find((o) => o.value === groupId)?.label ?? '所选分组'}」` : '已移出分组')
      void invalidate(true)
      onDone()
    } catch (e) {
      fail(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Select
      size="sm"
      aria-label="用户组"
      emptyOption="未分组（默认）"
      options={options}
      value={d.group_id ?? ''}
      disabled={busy || groups.isPending}
      onChange={(e) => void assign(e.target.value)}
      fieldClassName={css.groupSelect}
    />
  )
}
