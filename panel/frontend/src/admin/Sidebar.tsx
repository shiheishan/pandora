import { useCallback, useEffect, useRef, useState } from 'react'
import { HugeiconsIcon } from '@hugeicons/react'
import { href } from '../core/router'
import { MODULES, NAV_GROUPS, modulePath, type ModuleKey } from './modules'
import { NAV_ICONS, PORTAL_ICON } from './nav-icons'
import { taskCount, useDashboardTasks } from './tasks'
import css from './Sidebar.module.css'

function cx(...classes: Array<string | boolean | undefined | null>): string {
  return classes.filter(Boolean).join(' ')
}

// 侧栏徽标：GET v1/dashboard/tasks，与仪表盘「需要处理」共用同一个查询（./tasks）
function useNavBadges(): Partial<Record<ModuleKey, number>> {
  const { data } = useDashboardTasks()
  return { tickets: taskCount(data?.items, 'tickets_open'), marketing: taskCount(data?.items, 'withdrawals_pending') }
}

export function Sidebar({
  current,
  collapsed,
  drawer,
  offscreen,
}: {
  current: ModuleKey
  collapsed: boolean
  drawer: boolean
  /** 窄屏抽屉收着：移出屏幕，inert 让 Tab 与读屏都跳过它 */
  offscreen: boolean
}) {
  const badges = useNavBadges()
  const navRef = useRef<HTMLElement>(null)
  const [fadeTop, setFadeTop] = useState(false)
  const [fadeBottom, setFadeBottom] = useState(false)

  const updateFade = useCallback(() => {
    const el = navRef.current
    if (!el) return
    const top = el.scrollTop > 1
    const bottom = Math.ceil(el.scrollTop + el.clientHeight) < el.scrollHeight - 1
    setFadeTop((prev) => (prev !== top ? top : prev))
    setFadeBottom((prev) => (prev !== bottom ? bottom : prev))
  }, [])

  // ResizeObserver 在 observe 时会先回调一次，初值也由它算，不在 effect 里同步 setState
  useEffect(() => {
    const el = navRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(updateFade)
    ro.observe(el)
    return () => ro.disconnect()
  }, [updateFade])

  return (
    <aside id="admin-sidebar" className={cx(css.sidebar, collapsed && css.collapsed, drawer && css.open)} inert={offscreen || undefined}>
      <nav aria-label="主导航" ref={navRef} onScroll={updateFade} className={css.nav}>
        <div className={css.navGroups}>
          {NAV_GROUPS.map(([group, keys]) => (
            <div key={group} className={css.group}>
              <p className={css.groupTitle} aria-hidden={collapsed ? 'true' : undefined}>
                {group}
              </p>
              {keys.map((key) => {
                const on = key === current
                const badge = badges[key] ?? 0
                const title = MODULES[key].title
                const ariaLabel = collapsed ? (badge > 0 ? `${title} · ${badge}` : title) : undefined
                const tipText = badge > 0 ? `${title} · ${badge}` : title

                return (
                  <a
                    key={key}
                    href={href(modulePath(key))}
                    className={css.item}
                    aria-current={on ? 'page' : undefined}
                    aria-label={ariaLabel}
                  >
                    <span className={css.itemIcon}>
                      <HugeiconsIcon icon={NAV_ICONS[key]} size={16} strokeWidth={1.75} aria-hidden="true" />
                    </span>
                    <span className={css.label}>{title}</span>
                    {badge > 0 && <span className={css.badge}>{badge > 99 ? '99+' : badge}</span>}
                    {badge > 0 && <span className={css.dot} aria-hidden="true" />}
                    <span className={css.tip} aria-hidden="true">
                      {tipText}
                    </span>
                  </a>
                )
              })}
            </div>
          ))}
        </div>

        <div className={css.secondary}>
          <a
            href="../"
            target="_blank"
            rel="noopener"
            className={css.item}
            aria-label={collapsed ? '打开用户端' : undefined}
          >
            <span className={css.itemIcon}>
              <HugeiconsIcon icon={PORTAL_ICON} size={16} strokeWidth={1.75} aria-hidden="true" />
            </span>
            <span className={css.label}>打开用户端 ↗</span>
            <span className={css.tip} aria-hidden="true">
              打开用户端
            </span>
          </a>
        </div>
      </nav>

      {!collapsed && (
        <>
          <div aria-hidden="true" className={cx(css.fadeTop, fadeTop && css.fadeOn)} />
          <div aria-hidden="true" className={cx(css.fadeBottom, fadeBottom && css.fadeOn)} />
        </>
      )}
    </aside>
  )
}
