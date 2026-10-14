import { useEffect, useState } from 'react'
import { navigate, useHashLocation } from '../core/router'
import { ScreenFrame } from '../shell/ScreenFrame'
import { signOut, useRuntime } from '../shell/runtime'
import { Tabs } from '../ui'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { TelegramBindDialog } from './TelegramBindDialog'
import { CommandPalette } from './CommandPalette'
import { EventsCapsule } from './EventsCapsule'
import { ADMIN_IDLE_MS, startIdleWatch } from './idle'
import { useAdminMe } from './me'
import { MODULES, modulePath, resolveRoute, type ModuleKey } from './modules'
import { SCREENS } from './screens'
import css from './Shell.module.css'
import { Sidebar } from './Sidebar'

export function Shell() {
  const location = useHashLocation()
  const me = useAdminMe()
  const route = resolveRoute(location.path)
  const def = MODULES[route.module]
  const Screen = SCREENS[route.module]
  const [palette, setPalette] = useState(false)
  const [password, setPassword] = useState(false)
  const [telegram, setTelegram] = useState(false)
  const runtime = useRuntime()

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
    document.title = `${tab ? `${tab} · ` : ''}${def.title} · Pandora 控制台`
  }, [def, route.tab])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault()
        setPalette(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const go = (module: ModuleKey, tab?: string | null) => navigate(modulePath(module, tab))
  const tabs = def.tabs ?? []

  return (
    <div className={css.frame}>
      <Sidebar current={route.module} me={me.data} onOpenPalette={() => setPalette(true)} onChangePassword={() => setPassword(true)} onBindTelegram={() => setTelegram(true)} />
      <main className={css.main}>
        <header className={css.topbar}>
          <div className={css.crumbs}>
            <span>{def.group}</span>
            <span aria-hidden="true">/</span>
            <span className={css.crumbCurrent}>{def.title}</span>
          </div>
          <div className={css.spacer} />
          <EventsCapsule onNavigate={go} />
        </header>
        <div className={css.pageHead}>
          <h1 className={css.title}>{def.title}</h1>
          {tabs.length > 0 && route.tab && (
            <Tabs
              label={def.title}
              items={tabs.map(([value, label]) => ({ value, label }))}
              value={route.tab}
              onChange={(tab) => go(route.module, tab)}
            />
          )}
        </div>
        <div className={css.content}>
          <ScreenFrame resetKey={route.canonical}>
            <Screen tab={route.tab} rest={route.rest} />
          </ScreenFrame>
        </div>
      </main>
      <CommandPalette open={palette} onClose={() => setPalette(false)} onPick={(item) => go(item.module, item.tab)} />
      <ChangePasswordDialog open={password} onClose={() => setPassword(false)} />
      <TelegramBindDialog open={telegram} onClose={() => setTelegram(false)} />
    </div>
  )
}
