import { useEffect, useMemo, useState } from 'react'
import { navigate, parseHash, useHashLocation } from '../core/router'
import { ScreenFrame } from '../shell/ScreenFrame'
import { signOut, useRuntime } from '../shell/runtime'
import { Empty, Tabs } from '../ui'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { CommandPalette } from './CommandPalette'
import { EventsProvider } from './EventsCapsule'
import { ADMIN_IDLE_MS, startIdleWatch } from './idle'
import { cx, PageActionsProvider } from './kit'
import {
  hasOwnHead,
  isDetailRoute,
  isFullBleed,
  isPendingTab,
  MODULES,
  modulePath,
  resolveRoute,
  type ModuleKey,
} from './modules'
import { pageHelp } from './page-help'
import { PageHelpTip } from './PageHelp'
import { SCREENS } from './screens'
import css from './Shell.module.css'
import { Sidebar } from './Sidebar'
import { useAdminSiteName } from './site'
import { TelegramBindDialog } from './TelegramBindDialog'
import { Topbar } from './Topbar'

/** 媒体查询是否命中（SSR / 没有 matchMedia 时为 false） */
function useMedia(query: string): boolean {
  const [matches, setMatches] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
    return window.matchMedia(query).matches
  })
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mq = window.matchMedia(query)
    const onChange = () => setMatches(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [query])
  return matches
}

export function Shell() {
  const location = useHashLocation()
  const site = useAdminSiteName()
  const route = resolveRoute(location.path)
  const def = MODULES[route.module]
  const Screen = SCREENS[route.module]
  const narrow = useMedia('(max-width: 959px)')
  const phone = useMedia('(max-width: 639px)')

  const [drawer, setDrawer] = useState(false)
  const [palette, setPalette] = useState(false)
  const [password, setPassword] = useState(false)
  const [telegram, setTelegram] = useState(false)

  // 页头插槽（见 kit/PageActions）：手机上操作格换成顶栏右侧那格，标题行整行隐藏
  const [headActions, setHeadActions] = useState<HTMLElement | null>(null)
  const [topActions, setTopActions] = useState<HTMLElement | null>(null)
  const [countSlot, setCountSlot] = useState<HTMLElement | null>(null)

  const slots = useMemo(
    () => ({ actions: phone ? topActions : headActions, count: countSlot }),
    [phone, topActions, headActions, countSlot],
  )

  const runtime = useRuntime()

  // 换页就关抽屉（渲染期比较上一次地址，不在 effect 里 setState）
  const [prevPath, setPrevPath] = useState(location.path)
  if (location.path !== prevPath) {
    setPrevPath(location.path)
    setDrawer(false)
  }

  const navOpen = narrow && drawer

  // 换页回到顶部（同门户外框）：切模块、切页签、进出详情都从页头看起；只改查询串（筛选）时不动。
  // 不复位的话，往下滚过再切页签，新页签先画的短骨架会把滚动位置压下来，页头和页签栏跟着跳
  useEffect(() => {
    window.scrollTo(0, 0)
  }, [location.path])

  // 2 小时无操作自动退出（后台轮询会让服务端的空闲计时一直续上，见 idle.ts）
  useEffect(() => startIdleWatch({ timeoutMs: ADMIN_IDLE_MS, onIdle: () => void signOut(runtime) }), [runtime])

  // 未知模块、缺省标签一律改写成规范地址，刷新与分享都落在同一处；
  // 模块认得时查询串（页面的筛选条件）跟着走，落回仪表盘时丢掉
  // 外框是懒加载的：挂上前地址可能已经又变了（登录后紧接着跳页），这次渲染的地址已过时就不改写，
  // 交给下一次渲染，免得拿旧地址把新地址盖掉
  useEffect(() => {
    if (location.path === route.canonical) return
    if (parseHash(window.location.hash).path !== location.path) return
    const sameModule = location.path.split('/')[1] === route.module
    navigate(route.canonical, { replace: true, query: sameModule ? Object.fromEntries(location.query) : undefined })
  }, [location, route.canonical, route.module])

  useEffect(() => {
    const tab = def.tabs?.find(([k]) => k === route.tab)?.[1]
    document.title = `${tab ? `${tab} · ` : ''}${def.title} · ${site} 控制台`
  }, [def, route.tab, site])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && navOpen) {
        setDrawer(false)
        return
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPalette((v) => !v)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [navOpen])

  const go = (module: ModuleKey, tab?: string | null) => navigate(modulePath(module, tab))
  const tabs = def.tabs ?? []
  const help = pageHelp(route.module, route.tab)
  const pending = isPendingTab(route.module, route.tab)

  const detail = isDetailRoute(route.module, route.tab, route.rest)
  const bleed = isFullBleed(route.module)
  // 三种外框模式：详情（DetailPage 自己画头）、全幅（工单铺满）、自画页头（仪表盘）
  const showTitle = !detail && !bleed && !hasOwnHead(route.module)
  const showTabs = !detail && !bleed && tabs.length > 0

  return (
    <EventsProvider onOpen={(path) => navigate(path)}>
      <PageActionsProvider slots={slots}>
        <div className={css.frame}>
          <Sidebar
            current={route.module}
            open={navOpen}
            offscreen={narrow && !drawer}
            onOpenPalette={() => setPalette(true)}
            onChangePassword={() => setPassword(true)}
            onBindTelegram={() => setTelegram(true)}
          />
          {navOpen && <div className={css.scrim} onClick={() => setDrawer(false)} aria-hidden="true" />}
          <div className={css.main}>
            <Topbar title={def.title} navOpen={navOpen} onOpenNav={() => setDrawer(true)} actionsRef={setTopActions} />
            <main className={cx(css.page, bleed && css.bleed)}>
              <div className={bleed ? css.bleedCol : css.col}>
                {showTitle && (
                  <div className={css.head}>
                    <h1 className={css.title}>{def.title}</h1>
                    <span ref={setCountSlot} className={css.count} />
                    <span className={css.spacer} />
                    {help && <PageHelpTip help={help} />}
                    <div ref={setHeadActions} className={css.actions} />
                  </div>
                )}
                {showTabs && route.tab != null && (
                  <Tabs
                    className={css.tabs}
                    label={def.title}
                    items={tabs.map(([value, label]) => ({ value, label }))}
                    value={route.tab}
                    onChange={(tab) => go(route.module, tab)}
                  />
                )}
                {pending ? (
                  <Empty title="这一页正在按新设计重做" description="暂时还不能用，做好后会出现在这里。" />
                ) : (
                  <ScreenFrame resetKey={route.canonical}>
                    <Screen tab={route.tab} rest={route.rest} />
                  </ScreenFrame>
                )}
              </div>
            </main>
          </div>
          <CommandPalette open={palette} onClose={() => setPalette(false)} onPick={(item) => go(item.module, item.tab)} />
          <ChangePasswordDialog open={password} onClose={() => setPassword(false)} />
          <TelegramBindDialog open={telegram} onClose={() => setTelegram(false)} />
        </div>
      </PageActionsProvider>
    </EventsProvider>
  )
}
