import { href } from '../core/router'
import { toggleTheme, useTheme } from '../core/theme'
import { Logo } from '../shell/Logo'
import { signOut, useRuntime } from '../shell/runtime'
import { Menu } from '../ui'
import { identityLabels, type AdminMe } from './me'
import { MODULES, NAV_GROUPS, canReadModule, modulePath, type ModuleKey, type Permissions } from './modules'
import { taskCount, useDashboardTasks } from './tasks'
import css from './Sidebar.module.css'

// 侧栏徽标：GET v1/dashboard/tasks（后端已实现），只在有 ops.dashboard.read 时请求；
// 与仪表盘「需要处理」共用同一个查询（./tasks）。条目按各自读权限过滤，缺的条目就不显示徽标
function useNavBadges(enabled: boolean): Partial<Record<ModuleKey, number>> {
  const { data } = useDashboardTasks(enabled)
  return { tickets: taskCount(data?.items, 'tickets_open'), marketing: taskCount(data?.items, 'withdrawals_pending') }
}

export function Sidebar({
  current,
  me,
  perms,
  onOpenPalette,
  onChangePassword,
}: {
  current: ModuleKey
  me: AdminMe | undefined
  /** undefined = GET v1/me 还没回来，导航先不画，免得无权限的入口闪一下 */
  perms: Permissions | undefined
  onOpenPalette: () => void
  onChangePassword: () => void
}) {
  const runtime = useRuntime()
  const theme = useTheme()
  const badges = useNavBadges(me?.permissions.includes('ops.dashboard.read') ?? false)
  const who = identityLabels(me)

  return (
    <aside className={css.sidebar}>
      <div className={css.brand}>
        <Logo size={21} className={css.logo} />
        <span className={css.release}>{__APP_RELEASE__}</span>
      </div>
      <button type="button" className={css.search} onClick={onOpenPalette}>
        <span>搜索或跳转…</span>
        <kbd className={css.kbd}>⌘K</kbd>
      </button>
      <nav className={css.nav} aria-label="主导航">
        {NAV_GROUPS.map(([group, all]) => {
          const keys = perms ? all.filter((key) => canReadModule(key, perms)) : []
          if (keys.length === 0) return null
          return (
            <div key={group} className={css.group}>
              <div className={css.groupLabel}>{group}</div>
              {keys.map((key) => {
                const on = key === current
                const badge = badges[key] ?? 0
                return (
                  <a key={key} href={href(modulePath(key, null, perms))} className={on ? `${css.item} ${css.on}` : css.item} aria-current={on ? 'page' : undefined}>
                    <span className={css.dot} aria-hidden="true" />
                    <span className={css.itemLabel}>{MODULES[key].title}</span>
                    {badge > 0 && <span className={css.badge}>{badge > 99 ? '99+' : badge}</span>}
                  </a>
                )
              })}
            </div>
          )
        })}
      </nav>
      <Menu
        label="账户"
        placement="top"
        align="start"
        className={css.account}
        menuClassName={css.accountMenu}
        triggerClassName={css.accountButton}
        trigger={
          <>
            <span className={css.avatar} aria-hidden="true">
              {who.initial}
            </span>
            <span className={css.who}>
              <span className={css.whoTitle}>{who.title}</span>
              <span className={css.whoSub}>{who.subtitle}</span>
            </span>
            <span className={css.more} aria-hidden="true">
              ⋯
            </span>
          </>
        }
        entries={[
          { key: 'theme', label: `切换到${theme === 'dark' ? '浅色' : '深色'}模式`, onSelect: toggleTheme },
          { key: 'password', label: '修改我的密码', onSelect: onChangePassword },
          // 门户与后台同域，后台在前缀下，入口页的上一级就是门户根
          { key: 'portal', label: '打开用户门户 ↗', onSelect: () => window.open('../', '_blank', 'noopener') },
          { kind: 'separator', key: 'sep' },
          { key: 'logout', label: '退出登录', danger: true, onSelect: () => void signOut(runtime) },
        ]}
      />
    </aside>
  )
}
