/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../core/theme 的 useTheme / toggleTheme，依赖 ../core/router 的 href，依赖 ../shell/runtime 的 useApi / useRuntime / signOut，依赖 ../shell/Logo，依赖 ../ui 的 Menu，依赖 ./me 与 ./modules，依赖 ./Sidebar.module.css
 * [OUTPUT]: 对外提供 Sidebar
 * [POS]: admin 外框的深色侧栏（管理后台.dc.html aside）：字标与版本号、⌘K 入口、六组导航（工单 / 营销徽标取 GET v1/dashboard/tasks）、底部账户块与向上弹出的账户菜单（主题、改密码、打开门户、退出）
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { href } from '../core/router'
import { toggleTheme, useTheme } from '../core/theme'
import { Logo } from '../shell/Logo'
import { signOut, useApi, useRuntime } from '../shell/runtime'
import { Menu } from '../ui'
import { identityLabels, type AdminMe } from './me'
import { MODULES, NAV_GROUPS, modulePath, type ModuleKey } from './modules'
import css from './Sidebar.module.css'

// 侧栏徽标：待补·后端的 GET v1/dashboard/tasks，只在有 ops.dashboard.read 时请求；
// 条目按各自读权限过滤，缺的条目就不显示徽标
const tasksSchema = z.object({
  items: z.array(z.object({ kind: z.string(), count: z.number().optional() })),
})

function useNavBadges(enabled: boolean): Partial<Record<ModuleKey, number>> {
  const api = useApi()
  const { data } = useQuery({
    queryKey: ['admin', 'dashboard', 'tasks'],
    queryFn: ({ signal }) => api.get('v1/dashboard/tasks', tasksSchema, { signal }),
    enabled,
    meta: { topics: ['tickets.changed', 'orders.changed'] },
    refetchInterval: 60_000,
  })
  const count = (kind: string) => data?.items.find((i) => i.kind === kind)?.count ?? 0
  return { tickets: count('tickets_open'), marketing: count('withdrawals_pending') }
}

export function Sidebar({
  current,
  me,
  onOpenPalette,
  onChangePassword,
}: {
  current: ModuleKey
  me: AdminMe | undefined
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
        {NAV_GROUPS.map(([group, keys]) => (
          <div key={group} className={css.group}>
            <div className={css.groupLabel}>{group}</div>
            {keys.map((key) => {
              const on = key === current
              const badge = badges[key] ?? 0
              return (
                <a key={key} href={href(modulePath(key))} className={on ? `${css.item} ${css.on}` : css.item} aria-current={on ? 'page' : undefined}>
                  <span className={css.dot} aria-hidden="true" />
                  <span className={css.itemLabel}>{MODULES[key].title}</span>
                  {badge > 0 && <span className={css.badge}>{badge > 99 ? '99+' : badge}</span>}
                </a>
              )
            })}
          </div>
        ))}
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
