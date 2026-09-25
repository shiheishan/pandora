/**
 * [INPUT]: 依赖 react 的 useState，依赖 ../../../core/api 的 isApiError，依赖 ../../../core/format 的 formatDateTime，依赖 ../../../core/router 的 navigate，依赖 ../../../ui 的 Button / Drawer / Empty / Skeleton / Tabs / Tag，依赖 ../../actions 的 useCan，依赖 ./api 的 useUser / useInvalidateUsers / UserDetail，依赖 ./dialogs，依赖 ./tabs，依赖 ./Resets 的 ResetHistory，依赖 ./RiskTab，依赖 ./model，依赖 ./Users.module.css
 * [OUTPUT]: 对外提供 UserDrawer、DRAWER_TABS 与 DrawerTab
 * [POS]: 用户详情抽屉（560 宽，设计稿后台-03）：头部（首字头像、邮箱、状态、封禁标注、#id · 注册于）、操作条（启用 / 停用、重置密码、调整余额、更换订阅地址、为其开单——跳订单页人工开单弹窗 #/billing/orders?new=<id>，各按权限出现）、行内调账表单与标签页（画像 / 订阅 / 设备 / 流量重置 / 订单，流量重置要 metering.reset.read，持 security.audit.read 时加「风控」）；标签页在地址的第二段 #/users/list/<id>/<tab>，可深链
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useState } from 'react'
import { isApiError } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { navigate } from '../../../core/router'
import { Button, Drawer, Empty, Skeleton, Tabs, Tag } from '../../../ui'
import { useCan } from '../../actions'
import { useInvalidateUsers, useUser, type UserDetail } from './api'
import { BalanceForm, ResetPasswordDialog, RotateDialog, StatusDialog } from './dialogs'
import { initial, shortId, USER_STATUS_VIEW } from './model'
import { ResetHistory } from './Resets'
import { RiskTab } from './RiskTab'
import { DevicesTab, OrdersTab, ProfileTab, SubscriptionsTab } from './tabs'
import css from './Users.module.css'

export const DRAWER_TABS = [
  ['profile', '画像'],
  ['sub', '订阅'],
  ['devices', '设备'],
  ['resets', '流量重置'],
  ['orders', '订单'],
  ['risk', '风控'],
] as const
export type DrawerTab = (typeof DRAWER_TABS)[number][0]

export function isDrawerTab(v: string | undefined): v is DrawerTab {
  return DRAWER_TABS.some(([k]) => k === v)
}

export function UserDrawer({ id, tab, onTab, onClose, now }: { id: string | null; tab: DrawerTab; onTab: (t: DrawerTab) => void; onClose: () => void; now: Date }) {
  const q = useUser(id)
  const d = q.data
  return (
    <Drawer
      open={id !== null}
      onClose={onClose}
      width={560}
      title={d ? <DrawerTitle d={d} /> : '用户详情'}
      subtitle={d ? `#${shortId(d.id)} · 注册于 ${formatDateTime(d.created_at).slice(0, 10)}` : undefined}
    >
      {q.isPending ? (
        <div className={css.drawerSkeleton} role="status" aria-label="加载中">
          <Skeleton height={30} />
          <Skeleton height={80} />
          <Skeleton height={160} />
        </div>
      ) : q.isError ? (
        isApiError(q.error, 'not_found') ? (
          <Empty bare title="用户不存在" description="它可能已被删除，或链接有误。" />
        ) : (
          <Empty
            bare
            title="用户详情读取失败"
            description={q.error.message}
            action={
              <Button size="sm" onClick={() => void q.refetch()}>
                重试
              </Button>
            }
          />
        )
      ) : (
        <Body key={q.data.id} d={q.data} tab={tab} onTab={onTab} now={now} />
      )}
    </Drawer>
  )
}

function DrawerTitle({ d }: { d: UserDetail }) {
  const st = USER_STATUS_VIEW[d.status]
  return (
    <span className={css.drawerTitle}>
      <span className={css.avatarLg} aria-hidden="true">
        {initial(d.email, d.display_name)}
      </span>
      <span className={css.drawerEmail}>{d.email}</span>
      <Tag tone={st.tone}>{st.label}</Tag>
      {d.status === 'banned' && <Tag tone="danger">封禁</Tag>}
    </span>
  )
}

type Dialog = 'status' | 'password' | 'rotate' | null

function Body({ d, tab, onTab, now }: { d: UserDetail; tab: DrawerTab; onTab: (t: DrawerTab) => void; now: Date }) {
  const can = useCan()
  const invalidate = useInvalidateUsers()
  const [dialog, setDialog] = useState<Dialog>(null)
  const [balanceOpen, setBalanceOpen] = useState(false)
  const canWrite = can('iam.user.write')
  const canRisk = can('security.audit.read')
  const canResets = can('metering.reset.read')
  const tabs = DRAWER_TABS.filter(([k]) => (k !== 'risk' || canRisk) && (k !== 'resets' || canResets))
  const current: DrawerTab = tabs.some(([k]) => k === tab) ? tab : 'profile'
  const disabled = d.status === 'suspended' || d.status === 'banned'
  // 待验证、注销中、已匿名的账号不给启停：后端只收 active / suspended / banned 之间的切换语义
  const toggleable = d.status === 'active' || disabled
  // 只读账号一个操作都没有时，不留空的操作条
  const hasActions = canWrite || can('billing.provider.write') || can('billing.order.write')

  return (
    <div className={css.drawerBody}>
      {hasActions && (
        <div className={css.actionBar}>
          {canWrite && toggleable && (
            <Button size="sm" className={disabled ? undefined : css.dangerText} onClick={() => setDialog('status')}>
              {disabled ? '启用账号' : '禁用账号'}
            </Button>
          )}
          {canWrite && (
            <Button size="sm" onClick={() => setDialog('password')}>
              重置密码
            </Button>
          )}
          {can('billing.provider.write') && (
            <Button size="sm" aria-expanded={balanceOpen} onClick={() => setBalanceOpen((v) => !v)}>
              调整余额
            </Button>
          )}
          {canWrite && d.subscriptions.length > 0 && (
            <Button size="sm" onClick={() => setDialog('rotate')}>
              更换订阅地址
            </Button>
          )}
          {can('billing.order.write') && (
            <Button size="sm" onClick={() => navigate('/billing/orders', { query: { new: d.id } })}>
              为其开单
            </Button>
          )}
        </div>
      )}
      {balanceOpen && <BalanceForm user={d} onClose={() => setBalanceOpen(false)} onDone={() => void invalidate()} />}
      <Tabs label="用户详情" items={tabs.map(([value, label]) => ({ value, label }))} value={current} onChange={(v) => onTab(v as DrawerTab)} />
      <div className={css.tabPanel} role="tabpanel">
        {current === 'profile' && <ProfileTab d={d} now={now} />}
        {current === 'sub' && <SubscriptionsTab d={d} now={now} />}
        {current === 'devices' && <DevicesTab d={d} />}
        {current === 'resets' && canResets && <ResetHistory d={d} />}
        {current === 'orders' && <OrdersTab d={d} now={now} />}
        {current === 'risk' && canRisk && <RiskTab userId={d.id} now={now} />}
      </div>
      <StatusDialog user={d} open={dialog === 'status'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
      <ResetPasswordDialog user={d} open={dialog === 'password'} onClose={() => setDialog(null)} />
      <RotateDialog user={d} open={dialog === 'rotate'} onClose={() => setDialog(null)} onDone={() => void invalidate()} />
    </div>
  )
}
