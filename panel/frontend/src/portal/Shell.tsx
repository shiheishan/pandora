import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  useSyncExternalStore,
  type KeyboardEvent,
} from 'react'
import { formatMoney } from '../core/format'
import { href, navigate, useHashLocation } from '../core/router'
import { setTheme, useTheme } from '../core/theme'
import { ScreenFrame } from '../shell/ScreenFrame'
import { signOut, useRealtime, useRuntime } from '../shell/runtime'
import { PageHeadProvider, type PageHead } from './head'
import { Avatar, Chip, Icon, Num, cx, type IconName } from './kit'
import {
  HIDDEN_TITLE_PAGES,
  HOME_PAGE,
  PAGES,
  SIDE_GROUPS,
  TAB_PAGES,
  WIDE_PAGES,
  pagePath,
  resolvePage,
  sideOwner,
  tabLabel,
  tabOwner,
  type PageKey,
} from './pages'
import {
  displayName,
  useActivePlanName,
  useBalance,
  usePendingOrderCount,
  usePortalMe,
  useUnreadCount,
} from './queries'
import { SCREENS } from './screens'
import css from './Shell.module.css'
import { SiteBrand } from './SiteBrand'

// 页面 → 样稿的图标语义名（侧栏与底部标签栏共用）
const PAGE_ICONS: Readonly<Record<PageKey, IconName>> = {
  subs: 'subs',
  plans: 'plans',
  wallet: 'wallet',
  orders: 'orders',
  referral: 'gift',
  messages: 'bell',
  tickets: 'chat',
  help: 'help',
  account: 'shield',
  me: 'user',
  checkout: 'plans',
}

const DESKTOP_MQ = '(min-width: 960px)'

function subscribeDesktop(onStoreChange: () => void) {
  const mq = window.matchMedia(DESKTOP_MQ)
  mq.addEventListener('change', onStoreChange)
  return () => mq.removeEventListener('change', onStoreChange)
}

function getDesktopSnapshot() {
  return window.matchMedia(DESKTOP_MQ).matches
}

/** 桌面（≥ 960）：只用来把 #/me 落回我的套餐；侧栏与标签栏的显隐由 CSS 断点管 */
function useDesktop(): boolean {
  return useSyncExternalStore(subscribeDesktop, getDesktopSnapshot, () => false)
}

function sideLinkAriaLabel(key: PageKey, pending: number | null, unread: number): string | undefined {
  if (key === 'orders' && pending != null && pending > 0) return `订单，${pending} 笔待支付`
  if (key === 'messages' && unread > 0) return `消息，${unread} 条未读`
  return undefined
}

// 侧栏底部的账户按钮与菜单（样稿 .me-wrap / .acct-menu）：点开在按钮上方弹出；
// 点外面、Esc、Tab、换页都收起，方向键在菜单项之间移动
function AccountMenu({
  name,
  email,
  plan,
  balance,
  path,
}: {
  name: string
  email: string
  plan: string | null
  balance: string | null
  path: string
}) {
  const runtime = useRuntime()
  const theme = useTheme()
  const menuId = useId()
  // 记下在哪一页打开的：换页后自然算收起，不用在 effect 里 setState
  const [openOn, setOpenOn] = useState<string | null>(null)
  const open = openOn === path
  const setOpen = useCallback((next: boolean) => setOpenOn(next ? path : null), [path])
  const wrapRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  const close = useCallback(() => setOpen(false), [setOpen])

  useEffect(() => {
    if (!open) return
    menuRef.current?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus()
  }, [open])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open, setOpen])

  const focusMenuItem = (delta: number) => {
    const items = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]') ?? [])
    if (items.length === 0) return
    const i = items.indexOf(document.activeElement as HTMLElement)
    const next = items[(i < 0 ? 0 : i + delta + items.length) % items.length]
    next?.focus()
  }

  const onMenuKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      setOpen(false)
      triggerRef.current?.focus()
      return
    }
    if (e.key === 'Tab') {
      setOpen(false)
      return
    }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      focusMenuItem(1)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      focusMenuItem(-1)
    } else if (e.key === 'Home') {
      e.preventDefault()
      menuRef.current?.querySelector<HTMLElement>('[role^="menuitem"]')?.focus()
    } else if (e.key === 'End') {
      e.preventDefault()
      const items = menuRef.current?.querySelectorAll<HTMLElement>('[role^="menuitem"]')
      items?.[items.length - 1]?.focus()
    }
  }

  const onTriggerKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      e.preventDefault()
      setOpen(true)
    }
  }

  return (
    <div ref={wrapRef} className={css.acctWrap}>
      {open && (
        <div ref={menuRef} id={menuId} role="menu" aria-label="账户" className={css.acctMenu} onKeyDown={onMenuKeyDown}>
          <div className={css.amHead}>
            <b>
              {name}
              {plan && <Chip>{plan}</Chip>}
            </b>
            <small>{email}</small>
          </div>
          <a role="menuitem" tabIndex={-1} href={href('/wallet')} className={css.amItem} onClick={close}>
            <Icon name="wallet" size={17} />
            钱包
            {balance && <Num className={css.amValue}>{balance}</Num>}
          </a>
          <a role="menuitem" tabIndex={-1} href={href('/account')} className={css.amItem} onClick={close}>
            <Icon name="shield" size={17} />
            账号安全
          </a>
          <div role="group" aria-label="外观" className={cx(css.amItem, css.amTheme)}>
            <Icon name="moon" size={17} />
            外观
            <span className={css.amSeg}>
              <button
                type="button"
                role="menuitemradio"
                aria-checked={theme === 'light'}
                tabIndex={-1}
                className={cx(css.amSegOpt, theme === 'light' && css.amSegOn)}
                onClick={() => setTheme('light')}
              >
                浅色
              </button>
              <button
                type="button"
                role="menuitemradio"
                aria-checked={theme === 'dark'}
                tabIndex={-1}
                className={cx(css.amSegOpt, theme === 'dark' && css.amSegOn)}
                onClick={() => setTheme('dark')}
              >
                深色
              </button>
            </span>
          </div>
          <div role="separator" className={css.amSep} />
          <button type="button" role="menuitem" tabIndex={-1} className={cx(css.amItem, css.amOut)} onClick={() => void signOut(runtime)}>
            <Icon name="logout" size={17} />
            退出登录
          </button>
        </div>
      )}
      <button
        ref={triggerRef}
        type="button"
        className={css.me}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen(!open)}
        onKeyDown={onTriggerKeyDown}
      >
        <Avatar name={name || email} size="sm" />
        <span className={css.who}>
          <b>{name}</b>
          {balance && (
            <span>
              余额 <Num>{balance}</Num>
            </span>
          )}
        </span>
        <Icon name="updown" size={16} className={css.meChev} />
      </button>
    </div>
  )
}

export function Shell() {
  const location = useHashLocation()
  const desktop = useDesktop()
  const { page, rest, canonical } = resolvePage(location.path, { desktop })
  const me = usePortalMe()
  const balance = useBalance()
  const plan = useActivePlanName()
  const pending = usePendingOrderCount()
  const unread = useUnreadCount()
  const [head, setHead] = useState<PageHead | null>(null)
  const [aside, setAside] = useState<HTMLElement | null>(null)
  const steps = useRef(-1)
  useRealtime(true)

  useEffect(() => {
    steps.current += 1
  }, [location.path, location.query])

  useEffect(() => {
    if (location.path === canonical) return
    navigate(canonical, {
      replace: true,
      query: location.path.split('/')[1] === page ? Object.fromEntries(location.query) : undefined,
    })
  }, [location, canonical, page])

  useEffect(() => {
    window.scrollTo(0, 0)
  }, [location.path])

  const name = displayName(me.data)
  const balanceLabel = balance.data ? formatMoney(balance.data.balance) : null
  const Screen = SCREENS[page]
  const side = sideOwner(page)
  const tab = tabOwner(page)
  const meTabAlert = unread > 0 || (pending != null && pending > 0)

  const back = () => {
    if (steps.current > 0) window.history.back()
    else navigate(head?.parent ?? pagePath(HOME_PAGE))
  }

  return (
    <div className={css.app}>
      <aside className={css.side}>
        <a href={href(pagePath(HOME_PAGE))} className={css.logo} aria-label="我的套餐">
          <SiteBrand size={24} />
        </a>
        <nav className={css.nav} aria-label="主导航">
          {SIDE_GROUPS.map((group, i) => (
            <div key={i} className={css.navGroup}>
              {group.map((key) => {
                const count = key === 'orders' ? pending : key === 'messages' ? unread : null
                const showCount = count != null && count > 0
                const aria = sideLinkAriaLabel(key, pending, unread)
                return (
                  <a
                    key={key}
                    href={href(pagePath(key))}
                    className={cx(css.navA, side === key && css.navOn)}
                    aria-current={side === key ? 'page' : undefined}
                    aria-label={aria}
                  >
                    <Icon name={PAGE_ICONS[key]} />
                    {PAGES[key]}
                    {showCount && (
                      <span className={css.count} aria-hidden="true">
                        {count! > 99 ? '99+' : count}
                      </span>
                    )}
                  </a>
                )
              })}
            </div>
          ))}
        </nav>
        <AccountMenu
          name={name}
          email={me.data?.email ?? ''}
          plan={plan}
          balance={balanceLabel}
          path={location.path}
        />
      </aside>

      <main className={css.main}>
        <div key={page} className={cx(css.col, WIDE_PAGES.has(page) && css.wide)}>
          <div className={css.mTop}>
            <a href={href(pagePath(HOME_PAGE))} className={css.mLogo} aria-label="我的套餐">
              <SiteBrand size={22} />
            </a>
          </div>
          {head?.parent && (
            <button type="button" className={css.back} onClick={back}>
              ‹ 返回
            </button>
          )}
          <div className={cx(css.hRow, HIDDEN_TITLE_PAGES.has(page) && css.hRowBare)}>
            <h1 className={cx(css.title, HIDDEN_TITLE_PAGES.has(page) && css.srOnly)}>{head?.title ?? PAGES[page]}</h1>
            <div ref={setAside} className={css.headAside} />
          </div>
          <ScreenFrame resetKey={canonical}>
            <PageHeadProvider set={setHead} aside={aside}>
              <Screen rest={rest} />
            </PageHeadProvider>
          </ScreenFrame>
        </div>
      </main>

      <nav className={css.tabbar} aria-label="底部导航">
        {TAB_PAGES.map((key) => {
          const on = tab === key
          const meAria = key === 'me' && meTabAlert ? '我的，有未读消息或待支付订单' : undefined
          return (
            <a
              key={key}
              href={href(pagePath(key))}
              className={cx(css.tab, on && css.tabOn)}
              aria-current={on ? 'page' : undefined}
              aria-label={meAria}
            >
              <Icon name={PAGE_ICONS[key]} />
              {tabLabel(key)}
              {key === 'me' && meTabAlert && <span className={css.tabDot} aria-hidden="true" />}
            </a>
          )
        })}
      </nav>
    </div>
  )
}
