import { Moon02Icon, Search01Icon, SidebarLeftIcon, Sun03Icon } from '@hugeicons-pro/core-stroke-rounded'
import { HugeiconsIcon } from '@hugeicons/react'
import { toggleTheme, useTheme } from '../core/theme'
import { Logo } from '../shell/Logo'
import { signOut, useRuntime } from '../shell/runtime'
import { Menu } from '../ui'
import { EventsCapsule } from './EventsCapsule'
import { identityLabels, useAdminMe } from './me'
import type { ModuleKey } from './modules'
import { useAdminSiteName } from './site'
import css from './Topbar.module.css'

export interface TopbarProps {
  collapsed: boolean
  onToggleSidebar: () => void
  onOpenPalette: () => void
  onNavigate: (module: ModuleKey, tab?: string | null) => void
  onChangePassword: () => void
  onBindTelegram: () => void
}

export function Topbar({
  collapsed,
  onToggleSidebar,
  onOpenPalette,
  onNavigate,
  onChangePassword,
  onBindTelegram,
}: TopbarProps) {
  const me = useAdminMe()
  const siteName = useAdminSiteName()
  const theme = useTheme()
  const runtime = useRuntime()
  const who = identityLabels(me.data)

  const isDark = theme === 'dark'
  const themeLabel = isDark ? '切换到浅色模式' : '切换到深色模式'
  const sidebarLabel = collapsed ? '展开侧栏' : '收起侧栏'

  return (
    <header className={css.topbar}>
      <span className={css.brandLogo}>
        <Logo size={18} wordmark={false} />
      </span>

      <span className={css.brandText}>
        <span className={css.siteName} title={siteName}>
          {siteName}
        </span>
        <span className={css.release}>{__APP_RELEASE__}</span>
      </span>

      <button
        type="button"
        className={`${css.iconButton} ${css.toggleSidebar}`}
        onClick={onToggleSidebar}
        aria-label={sidebarLabel}
        title={sidebarLabel}
        aria-controls="admin-sidebar"
        aria-expanded={!collapsed}
      >
        <HugeiconsIcon icon={SidebarLeftIcon} size={16} strokeWidth={1.75} aria-hidden="true" />
      </button>

      <div className={css.searchWrap}>
        <button
          type="button"
          className={css.searchButton}
          onClick={onOpenPalette}
          aria-label="搜索或跳转（⌘K）"
        >
          <HugeiconsIcon
            icon={Search01Icon}
            size={16}
            strokeWidth={1.75}
            className={css.searchIcon}
            aria-hidden="true"
          />
          <span className={css.searchText}>搜索用户、订单、节点，或跳转到模块</span>
          <kbd className={css.searchKbd}>⌘K</kbd>
        </button>
      </div>

      <button
        type="button"
        className={css.iconButton}
        onClick={toggleTheme}
        aria-label={themeLabel}
        title={themeLabel}
      >
        <HugeiconsIcon
          icon={isDark ? Sun03Icon : Moon02Icon}
          size={16}
          strokeWidth={1.75}
          aria-hidden="true"
        />
      </button>

      <EventsCapsule onNavigate={(mod, tab) => onNavigate(mod, tab ?? null)} />

      <div className={css.accountMenuWrap}>
        <Menu
          label="账户"
          align="end"
          triggerLabel={`${who.title}，账户菜单`}
          triggerClassName={css.avatarButton}
          menuClassName={css.accountMenu}
          trigger={who.initial}
          header={
            <div className={css.accountHeader}>
              <div className={css.whoTitle}>{who.title}</div>
              <div className={css.whoSubtitle} title={who.subtitle}>
                {who.subtitle}
              </div>
            </div>
          }
          entries={[
            {
              key: 'password',
              label: '修改我的密码',
              onSelect: onChangePassword,
            },
            {
              key: 'telegram',
              label: '绑定 Telegram',
              onSelect: onBindTelegram,
            },
            {
              kind: 'separator',
              key: 'sep',
            },
            {
              key: 'logout',
              label: '退出登录',
              danger: true,
              onSelect: () => void signOut(runtime),
            },
          ]}
        />
      </div>
    </header>
  )
}
