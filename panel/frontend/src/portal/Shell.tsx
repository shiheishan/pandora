/**
 * [INPUT]: 依赖 react 的 state / effect，依赖 ../core/router 的 useHashLocation / navigate / href，依赖 ../core/theme，依赖 ../core/format 的 formatMoney，依赖 ../shell/runtime 的 useRuntime / useRealtime / signOut，依赖 ../shell/Logo，依赖 ../ui 的 Menu / Tag / CountBadge / Empty / IconChevronDown，依赖 ./pages、./queries，依赖 ./Shell.module.css
 * [OUTPUT]: 对外提供 Shell
 * [POS]: portal 登录后的外框（用户门户.dc.html showApp）：粘性顶栏（字标、四项导航、余额胶囊、消息铃铛、头像菜单）、页头、内容区、页脚；< 640 导航收进底部五格标签栏，「我的」打开头像菜单；门户的 SSE 在这里连上，只驱动查询失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { formatMoney } from '../core/format'
import { href, navigate, useHashLocation } from '../core/router'
import { toggleTheme, useTheme } from '../core/theme'
import { Logo } from '../shell/Logo'
import { signOut, useRealtime, useRuntime } from '../shell/runtime'
import { CountBadge, Empty, IconChevronDown, Menu, Tag } from '../ui'
import { MENU_PAGES, NAV_PAGES, PAGES, greeting, navLabel, navOwner, pagePath, resolvePage, type PageKey } from './pages'
import { displayName, useActivePlanName, useBalance, useCommissionAvailable, usePortalMe, useUnreadCount } from './queries'
import css from './Shell.module.css'

export function Shell() {
  const runtime = useRuntime()
  const location = useHashLocation()
  const { page, canonical } = resolvePage(location.path)
  const theme = useTheme()
  const me = usePortalMe()
  const balance = useBalance()
  const plan = useActivePlanName()
  const commission = useCommissionAvailable()
  const unread = useUnreadCount()
  const [menuOpen, setMenuOpen] = useState(false)
  useRealtime(true)

  useEffect(() => {
    if (location.path !== canonical) navigate(canonical, { replace: true })
  }, [location.path, canonical])

  // 换页回到顶部；菜单项与标签栏点击时 Menu 自己会关
  useEffect(() => {
    window.scrollTo(0, 0)
  }, [page])

  const name = displayName(me.data)
  const owner = navOwner(page)
  const inMenu = MENU_PAGES.includes(page)
  const balanceLabel = balance.data ? formatMoney(balance.data.balance, balance.data.currency) : null
  const [title, subtitle] = PAGES[page]
  const heading = page === 'overview' ? `${greeting()}${name ? `，${name}` : ''}` : title

  const hints: Partial<Record<PageKey, string | null>> = {
    wallet: balanceLabel,
    referral: commission.data ? formatMoney(commission.data.available, commission.data.currency) : null,
  }

  return (
    <div className={css.app}>
      <header className={css.header}>
        <div className={css.bar}>
          <a href={href(pagePath('overview'))} className={css.home} aria-label="概览">
            <Logo size={22} className={css.logo} />
          </a>
          <nav className={css.nav} aria-label="主导航">
            {NAV_PAGES.map((key) => (
              <a key={key} href={href(pagePath(key))} className={key === owner ? `${css.navItem} ${css.navOn}` : css.navItem} aria-current={key === owner ? 'page' : undefined}>
                {navLabel(key)}
              </a>
            ))}
          </nav>
          <div className={css.tools}>
            {balanceLabel && (
              <a href={href(pagePath('wallet'))} className={css.balance} title="钱包">
                {balanceLabel}
              </a>
            )}
            <a href={href(pagePath('messages'))} className={page === 'messages' ? `${css.bell} ${css.bellOn}` : css.bell} aria-label={unread ? `消息，${unread} 条未读` : '消息'}>
              <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
                <path d="M3.5 11.5V7a4.5 4.5 0 0 1 9 0v4.5l1 1H2.5z" className={css.bellStroke} />
                <path d="M6.5 14.2a1.6 1.6 0 0 0 3 0" className={css.bellStroke} />
              </svg>
              <CountBadge count={unread} className={css.bellBadge} />
            </a>
            <Menu
              label="账户与更多功能"
              open={menuOpen}
              onOpenChange={setMenuOpen}
              triggerClassName={menuOpen || inMenu ? `${css.avatarButton} ${css.avatarOn}` : css.avatarButton}
              trigger={
                <>
                  <span className={css.avatar} aria-hidden="true">
                    {(name || me.data?.email || '·').slice(0, 1).toUpperCase()}
                  </span>
                  <span className={css.avatarName}>{name}</span>
                  <IconChevronDown className={menuOpen ? `${css.chevron} ${css.chevronUp}` : css.chevron} />
                </>
              }
              header={
                <>
                  <span className={css.menuName}>
                    <span className={css.menuUser}>{name}</span>
                    {plan && <Tag tone="brand">{plan}</Tag>}
                  </span>
                  <span className={css.menuEmail}>{me.data?.email}</span>
                </>
              }
              entries={[
                ...MENU_PAGES.map((key) => ({ key, label: PAGES[key][0], hint: hints[key] ?? undefined, current: key === page, onSelect: () => navigate(pagePath(key)) })),
                { kind: 'separator' as const, key: 'sep' },
                { kind: 'toggle' as const, key: 'theme', label: '深色模式', checked: theme === 'dark', onChange: () => toggleTheme() },
                { key: 'logout', label: '退出登录', danger: true, onSelect: () => void signOut(runtime) },
              ]}
            />
          </div>
        </div>
      </header>

      <main className={css.main}>
        <div>
          <h1 className={css.title}>{heading}</h1>
          {subtitle && <p className={css.subtitle}>{subtitle}</p>}
        </div>
        <Empty title="这里还是空的" description={`「${title}」页面将在第 3 阶段接入。`} />
      </main>

      <footer className={css.footer}>
        {MENU_PAGES.map((key) => (
          <a key={key} href={href(pagePath(key))} className={key === page ? `${css.footLink} ${css.footOn}` : css.footLink}>
            {PAGES[key][0]}
          </a>
        ))}
        <span className={css.spacer} />
        <button type="button" className={css.footLink} onClick={() => toggleTheme()}>
          {theme === 'dark' ? '切换到浅色模式' : '切换到深色模式'}
        </button>
      </footer>

      <nav className={css.tabbar} aria-label="底部导航">
        {NAV_PAGES.map((key) => (
          <a key={key} href={href(pagePath(key))} className={key === owner ? `${css.tab} ${css.tabOn}` : css.tab} aria-current={key === owner ? 'page' : undefined}>
            <span className={css.tabLine} aria-hidden="true" />
            {navLabel(key)}
          </a>
        ))}
        <button
          type="button"
          data-menu-toggle=""
          className={menuOpen || inMenu ? `${css.tab} ${css.tabOn}` : css.tab}
          aria-expanded={menuOpen}
          onClick={() => {
            setMenuOpen(!menuOpen)
            window.scrollTo(0, 0)
          }}
        >
          <span className={css.tabLine} aria-hidden="true" />
          我的
        </button>
      </nav>
    </div>
  )
}
