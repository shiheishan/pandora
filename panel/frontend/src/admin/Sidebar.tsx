import { HugeiconsIcon } from '@hugeicons/react'
import { href } from '../core/router'
import { setTheme, useTheme } from '../core/theme'
import { Logo } from '../shell/Logo'
import { signOut, useRuntime } from '../shell/runtime'
import { IconButton, Menu } from '../ui'
import { EventsTrigger } from './EventsCapsule'
import { Avatar, cx } from './kit'
import { useAdminMe } from './me'
import { MODULES, NAV_GROUPS, modulePath, type ModuleKey } from './modules'
import {
  LOGOUT_ICON,
  NAV_ICONS,
  PASSWORD_ICON,
  PORTAL_ICON,
  SEARCH_ICON,
  TELEGRAM_ICON,
  THEME_ICONS,
} from './nav-icons'
import { useAdminSiteName } from './site'
import { formatWait, navBadges, ticketAlert, useDashboardTasks } from './tasks'
import css from './Sidebar.module.css'

export interface SidebarProps {
  current: ModuleKey
  /** 窄屏抽屉是否打开（样式用） */
  open: boolean
  /** 窄屏抽屉收着：整块 inert，Tab 与读屏都跳过 */
  offscreen: boolean
  onOpenPalette: () => void
  onChangePassword: () => void
  onBindTelegram: () => void
}

export function Sidebar(p: SidebarProps) {
  const { current, open, offscreen, onOpenPalette, onChangePassword, onBindTelegram } = p
  const siteName = useAdminSiteName()
  const theme = useTheme()
  const runtime = useRuntime()
  const me = useAdminMe().data
  const name = me?.display_name || me?.email.split('@')[0] || '管理员'
  const email = me?.email ?? ''

  const { data } = useDashboardTasks()
  const badges = navBadges(data?.items)
  const alert = ticketAlert(data?.items)

  const icon = (el: typeof PASSWORD_ICON) => (
    <HugeiconsIcon icon={el} size={16} strokeWidth={1.6} aria-hidden="true" />
  )

  return (
    <aside
      id="admin-sidebar"
      className={cx(css.side, open && css.open)}
      inert={offscreen || undefined}
    >
      <div className={css.team}>
        <Logo size={22} wordmark={false} />
        <b title={siteName}>{siteName}</b>
        <span className={css.badge}>{__APP_RELEASE__}</span>
      </div>

      <button type="button" className={css.find} onClick={onOpenPalette} aria-label="搜索或跳转（⌘K）">
        <HugeiconsIcon icon={SEARCH_ICON} size={16} strokeWidth={1.6} aria-hidden="true" />
        <span>搜索…</span>
        <kbd>⌘K</kbd>
      </button>

      <nav aria-label="主导航" className={css.nav}>
        <EventsTrigger variant="nav" />
        {NAV_GROUPS.map(([title, keys]) => (
          <div key={title ?? '_'}>
            {title != null && <p className={css.grp}>{title}</p>}
            {keys.map((key) => {
              const on = key === current
              const badge = badges[key]
              return (
                <a
                  key={key}
                  href={href(modulePath(key))}
                  className={css.na}
                  aria-current={on ? 'page' : undefined}
                >
                  <HugeiconsIcon icon={NAV_ICONS[key]} size={16} strokeWidth={1.6} aria-hidden="true" />
                  <span>{MODULES[key].title}</span>
                  {badge != null && (
                    // 数字不进链接的无障碍名称：浏览器用例按「工单」等精确名找导航链接；数量在仪表盘待办与提醒卡里读得到。
                    // 悬停提示写清数的是什么（如「超时未支付订单 2 个」），免得和页内别的「待处理」对错口径
                    <span className={cx(css.ct, badge.hot && css.hot)} aria-hidden="true" title={badge.label}>
                      {badge.count > 99 ? '99+' : badge.count}
                    </span>
                  )}
                </a>
              )
            })}
          </div>
        ))}
      </nav>

      {alert != null && current !== 'dash' && current !== 'tickets' && (
        <a href={href(modulePath('tickets'))} className={css.alert}>
          <b>
            <i aria-hidden="true" />
            {alert.count} 个工单没回复
          </b>
          {alert.oldestWaitSeconds != null && <span>最久的等了 {formatWait(alert.oldestWaitSeconds)}</span>}
        </a>
      )}

      <div className={css.foot}>
        <Menu
          label="账户"
          placement="top"
          align="start"
          className={css.acctWrap}
          triggerClassName={css.acct}
          menuClassName={css.acctMenu}
          triggerLabel={`${name}，账户菜单`}
          trigger={
            <>
              <Avatar text={name} />
              <span className={css.acctName}>{name}</span>
            </>
          }
          header={
            <div className={css.mh}>
              <b>{name}</b>
              <small>{email}</small>
            </div>
          }
          entries={[
            {
              key: 'password',
              label: '修改密码',
              icon: icon(PASSWORD_ICON),
              onSelect: onChangePassword,
            },
            {
              key: 'telegram',
              label: '绑定 Telegram',
              icon: icon(TELEGRAM_ICON),
              onSelect: onBindTelegram,
            },
            {
              kind: 'choice',
              key: 'theme',
              label: '外观',
              options: [
                { value: 'light', label: '浅色' },
                { value: 'dark', label: '深色' },
              ],
              value: theme,
              onChange: (v) => setTheme(v === 'dark' ? 'dark' : 'light'),
            },
            { kind: 'separator', key: 'sep' },
            {
              key: 'logout',
              label: '退出登录',
              icon: icon(LOGOUT_ICON),
              onSelect: () => void signOut(runtime),
            },
          ]}
        />
        <a className={css.ib} href="../" target="_blank" rel="noopener" aria-label="打开用户端" title="打开用户端">
          <HugeiconsIcon icon={PORTAL_ICON} size={16} strokeWidth={1.6} aria-hidden="true" />
        </a>
        <IconButton
          label={theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'}
          icon={THEME_ICONS[theme]}
          onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
          className={css.ib}
        />
      </div>
    </aside>
  )
}
