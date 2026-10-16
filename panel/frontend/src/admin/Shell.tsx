import { useEffect, useState } from 'react'
import { navigate, useHashLocation } from '../core/router'
import { ScreenFrame } from '../shell/ScreenFrame'
import { signOut, useRuntime } from '../shell/runtime'
import { Empty, Tabs } from '../ui'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { CommandPalette } from './CommandPalette'
import { ADMIN_IDLE_MS, startIdleWatch } from './idle'
import { MODULES, modulePath, resolveRoute, isPendingTab, type ModuleKey } from './modules'
import { pageHelp } from './page-help'
import { PageHelpTip } from './PageHelp'
import { SCREENS } from './screens'
import css from './Shell.module.css'
import { Sidebar } from './Sidebar'
import { useAdminSiteName } from './site'
import { TelegramBindDialog } from './TelegramBindDialog'
import { Topbar } from './Topbar'

const SIDEBAR_STORAGE_KEY = 'pandora-admin-sidebar'

/** 窄屏：侧栏不占位，用抽屉滑出（与 Topbar.module.css 断点一致） */
function useNarrow(): boolean {
  const [narrow, setNarrow] = useState(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
    return window.matchMedia('(max-width: 639px)').matches
  })
  useEffect(() => {
    const mq = window.matchMedia('(max-width: 639px)')
    const onChange = () => setNarrow(mq.matches)
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])
  return narrow
}

function getInitialSidebarCollapsed(): boolean {
  try {
    const saved = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    if (saved === 'icons') return true
    if (saved === 'expanded') return false
  } catch {
    // 忽略存储读取异常
  }
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    return window.matchMedia('(max-width: 959px)').matches
  }
  return false
}

function persistSidebarCollapsed(collapsed: boolean): void {
  try {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, collapsed ? 'icons' : 'expanded')
  } catch {
    // 忽略存储写入异常
  }
}

export function Shell() {
  const location = useHashLocation()
  const site = useAdminSiteName()
  const route = resolveRoute(location.path)
  const def = MODULES[route.module]
  const Screen = SCREENS[route.module]
  const narrow = useNarrow()
  const [collapsed, setCollapsed] = useState(getInitialSidebarCollapsed)
  const [drawer, setDrawer] = useState(false)
  const [palette, setPalette] = useState(false)
  const [password, setPassword] = useState(false)
  const [telegram, setTelegram] = useState(false)
  const runtime = useRuntime()

  // 换页就关抽屉（渲染期比较上一次地址，不在 effect 里 setState）
  const [prevPath, setPrevPath] = useState(location.path)
  if (location.path !== prevPath) {
    setPrevPath(location.path)
    setDrawer(false)
  }

  const toggleSidebar = () => {
    if (narrow) {
      setDrawer((v) => !v)
      return
    }
    persistSidebarCollapsed(!collapsed)
    setCollapsed(!collapsed)
  }

  const sidebarCollapsed = narrow ? false : collapsed
  const topbarCollapsed = narrow ? !drawer : collapsed

  // 2 小时无操作自动退出（后台轮询会让服务端的空闲计时一直续上，见 idle.ts）
  useEffect(() => startIdleWatch({ timeoutMs: ADMIN_IDLE_MS, onIdle: () => void signOut(runtime) }), [runtime])

  // 未知模块、缺省标签一律改写成规范地址，刷新与分享都落在同一处；
  // 模块认得时查询串（页面的筛选条件）跟着走，落回仪表盘时丢掉
  useEffect(() => {
    if (location.path === route.canonical) return
    const sameModule = location.path.split('/')[1] === route.module
    navigate(route.canonical, { replace: true, query: sameModule ? Object.fromEntries(location.query) : undefined })
  }, [location, route.canonical, route.module])

  useEffect(() => {
    const tab = def.tabs?.find(([k]) => k === route.tab)?.[1]
    document.title = `${tab ? `${tab} · ` : ''}${def.title} · ${site} 控制台`
  }, [def, route.tab, site])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && narrow && drawer) {
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
  }, [narrow, drawer])

  const go = (module: ModuleKey, tab?: string | null) => navigate(modulePath(module, tab))
  const tabs = def.tabs ?? []
  const help = pageHelp(route.module, route.tab)
  const pending = isPendingTab(route.module, route.tab)

  return (
    <div className={css.frame}>
      <Topbar
        narrow={narrow}
        collapsed={topbarCollapsed}
        onToggleSidebar={toggleSidebar}
        onOpenPalette={() => setPalette(true)}
        onChangePassword={() => setPassword(true)}
        onBindTelegram={() => setTelegram(true)}
      />
      <div className={css.body}>
        {narrow && drawer && (
          <div className={css.scrim} onClick={() => setDrawer(false)} aria-hidden="true" />
        )}
        <Sidebar current={route.module} collapsed={sidebarCollapsed} drawer={narrow && drawer} offscreen={narrow && !drawer} />
        <main className={css.main}>
          {(tabs.length > 0 || help) && (
            <div className={css.pageHead}>
              <div className={css.tabBar}>
                {tabs.length > 0 && route.tab && (
                  <Tabs
                    label={def.title}
                    items={tabs.map(([value, label]) => ({ value, label }))}
                    value={route.tab}
                    onChange={(tab) => go(route.module, tab)}
                  />
                )}
                {help && <PageHelpTip help={help} />}
              </div>
            </div>
          )}
          <div className={css.content}>
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
  )
}
