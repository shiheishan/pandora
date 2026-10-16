import { useState, type ReactNode } from 'react'
import { formatDateTime, formatMoney, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Select, StatStrip, Tag, useToast } from '../../../ui'
import { useFailure } from '../../actions'
import { okSchema, useInvalidateUsers, useUserGroups, useUserProfile, type UserDetail } from './api'
import css from './Detail.module.css'
import { ResetPasswordDialog, StatusDialog } from './dialogs'
import { DeleteDialog, EmailDialog, NoteDialog, ReferrerDialog } from './UserEditDialogs'
import { assignableGroups, groupOptionLabel, RISK_VIEW } from './model'

type Dialog = 'status' | 'password' | 'email' | 'note' | 'referrer' | 'delete' | null

export function ProfileTab({ d, now, onAdjustBalance }: { d: UserDetail; now: Date; onAdjustBalance: () => void }) {
  const invalidate = useInvalidateUsers()
  const profile = useUserProfile(d.id)
  const [dialog, setDialog] = useState<Dialog>(null)
  const deleted = d.status === 'deleted'
  const disabled = d.status === 'suspended' || d.status === 'banned'
  const toggleable = !deleted && (d.status === 'active' || disabled)
  const risk = RISK_VIEW[d.risk_level]

  const editBtn = (key: Dialog, label: string) =>
    !deleted ? (
      <Button size="xs" onClick={() => setDialog(key)}>
        {label}
      </Button>
    ) : null

  return (
    <>
      <StatStrip
        label="用户统计"
        items={[
          { label: '累计消费', value: formatMoney(d.stats.paid_total) },
          { label: '订单数', value: String(d.stats.order_count) },
          { label: '邀请人数', value: String(d.stats.referral_count) },
        ]}
      />

      <section className={css.section} aria-labelledby="profile-contact">
        <h3 id="profile-contact" className={css.sectionTitle}>登录与联系</h3>
        <dl className={css.facts}>
          <FactRow label="显示名" value={d.display_name || '—'} action={editBtn('note', '改显示名')} />
          <FactRow
            label="邮箱"
            value={
              <>
                {d.email}
                {d.email_verified ? <Tag tone="ok">已验证</Tag> : <Tag tone="warn">未验证</Tag>}
              </>
            }
            action={editBtn('email', '修改')}
          />
          <FactRow label="登录密码" value="已设置" action={editBtn('password', '重置')} />
          <FactRow
            label="Telegram"
            value={d.telegram ? `已绑定 @${d.telegram.username} · ${formatDateTime(d.telegram.bound_at).slice(0, 10)}` : '未绑定'}
          />
        </dl>
      </section>

      <section className={css.section} aria-labelledby="profile-funds">
        <h3 id="profile-funds" className={css.sectionTitle}>归属与资金</h3>
        <dl className={css.facts}>
          <div className={css.factRow}>
            <dt>用户组</dt>
            <dd>
              <GroupPicker d={d} />
            </dd>
          </div>
          <FactRow label="身份" value={d.is_admin ? '管理员' : '普通用户'} />
          <FactRow
            label="邀请人"
            value={
              d.referrer ? (
                <a href={href(`/users/list/${encodeURIComponent(d.referrer.id)}`)} className={css.link}>
                  {d.referrer.email}
                </a>
              ) : (
                '—'
              )
            }
            action={editBtn('referrer', '修改')}
          />
          <FactRow
            label="余额"
            value={<span className={css.mono}>{formatMoney(d.balance)}</span>}
            action={!deleted ? <Button size="xs" onClick={onAdjustBalance}>调整</Button> : null}
          />
        </dl>
      </section>

      <section className={css.section} aria-labelledby="profile-security">
        <h3 id="profile-security" className={css.sectionTitle}>安全</h3>
        <dl className={css.facts}>
          <FactRow label="风险等级" value={<Tag tone={risk.tone}>{risk.label}</Tag>} />
          <FactRow
            label="最近登录"
            value={d.last_login_at ? `${relativeTime(d.last_login_at, now)} · ${formatDateTime(d.last_login_at)}` : '从未登录'}
          />
          <FactRow
            label="注册 IP"
            value={
              <span className={css.mono}>
                {profile.isPending ? '…' : profile.data?.registered_ip || '无记录'}
              </span>
            }
          />
        </dl>
      </section>

      {!deleted && (toggleable || !d.is_admin) && (
        <section className={css.section} aria-label="账号处置">
          <div className={css.actions}>
            {toggleable && (
              <div className={css.actionRow}>
                <div className={css.actionText}>
                  <span className={css.actionLabel}>{disabled ? '启用账号' : '禁用账号'}</span>
                  <span className={css.actionDesc}>
                    {disabled ? '恢复登录与订阅连接。' : '暂停登录与订阅连接，数据保留，可随时启用。'}
                  </span>
                </div>
                <Button size="xs" className={disabled ? undefined : css.dangerText} onClick={() => setDialog('status')}>
                  {disabled ? '启用账号' : '禁用账号'}
                </Button>
              </div>
            )}
            {!d.is_admin && (
              <div className={css.actionRow}>
                <div className={css.actionText}>
                  <span className={css.actionLabel}>注销账号</span>
                  <span className={css.actionDesc}>匿名化资料并永久停用；订单与账本保留，不可撤销。</span>
                </div>
                <Button size="xs" className={css.dangerText} onClick={() => setDialog('delete')}>
                  注销账号
                </Button>
              </div>
            )}
          </div>
        </section>
      )}

      <StatusDialog user={d} open={dialog === 'status'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <ResetPasswordDialog user={d} open={dialog === 'password'} onClose={() => setDialog(null)} />
      <EmailDialog user={d} open={dialog === 'email'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <NoteDialog user={d} open={dialog === 'note'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <ReferrerDialog user={d} open={dialog === 'referrer'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <DeleteDialog user={d} open={dialog === 'delete'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
    </>
  )
}

function FactRow({ label, value, action }: { label: string; value: ReactNode; action?: ReactNode }) {
  return (
    <div className={css.factRow}>
      <dt>{label}</dt>
      <dd>
        <span className={css.factValue}>{value}</span>
        {action}
      </dd>
    </div>
  )
}

/** 用户组：POST v1/users/{id}/group（无幂等）；「未分组（默认）」传空串（契约） */
function GroupPicker({ d }: { d: UserDetail }) {
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
      disabled={busy || groups.isPending || d.status === 'deleted'}
      onChange={(e) => void assign(e.target.value)}
      fieldClassName={css.groupSelect}
    />
  )
}
