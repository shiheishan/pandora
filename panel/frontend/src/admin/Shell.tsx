/**
 * [INPUT]: 依赖 react 的 state / effect，依赖 ../core/router 的 useHashLocation / navigate，依赖 ../ui 的 Tabs / Empty，依赖 ./Sidebar、./EventsCapsule、./CommandPalette、./ChangePasswordDialog、./me、./modules，依赖 ./Shell.module.css
 * [OUTPUT]: 对外提供 Shell
 * [POS]: admin 登录后的外框（管理后台.dc.html showApp）：左侧 Sidebar，右侧粘性顶栏（面包屑 + 实时事件）、页头（标题 + 标签页）与模块内容区；模块页在第 3 阶段按 MODULES 接入，这里先放空状态
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from 'react'
import { navigate, useHashLocation } from '../core/router'
import { Empty, Tabs } from '../ui'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { CommandPalette } from './CommandPalette'
import { EventsCapsule } from './EventsCapsule'
import { useAdminMe } from './me'
import { MODULES, modulePath, resolveRoute, type ModuleKey } from './modules'
import css from './Shell.module.css'
import { Sidebar } from './Sidebar'

export function Shell() {
  const location = useHashLocation()
  const route = resolveRoute(location.path)
  const def = MODULES[route.module]
  const me = useAdminMe()
  const [palette, setPalette] = useState(false)
  const [password, setPassword] = useState(false)

  // 未知模块、缺省标签一律改写成规范地址，刷新与分享都落在同一处
  useEffect(() => {
    if (location.path !== route.canonical) navigate(route.canonical, { replace: true })
  }, [location.path, route.canonical])

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
  const tabTitle = def.tabs?.find(([k]) => k === route.tab)?.[1]

  return (
    <div className={css.frame}>
      <Sidebar current={route.module} me={me.data} onOpenPalette={() => setPalette(true)} onChangePassword={() => setPassword(true)} />
      <main className={css.main}>
        <header className={css.topbar}>
          <div className={css.crumbs}>
            <span>{def.group}</span>
            <span aria-hidden="true">/</span>
            <span className={css.crumbCurrent}>{def.title}</span>
          </div>
          <div className={css.spacer} />
          <EventsCapsule enabled={me.data?.permissions.includes('ops.notification.read') ?? false} onNavigate={go} />
        </header>
        <div className={css.pageHead}>
          <h1 className={css.title}>{def.title}</h1>
          {def.tabs && route.tab && (
            <Tabs
              label={def.title}
              items={def.tabs.map(([value, label]) => ({ value, label }))}
              value={route.tab}
              onChange={(tab) => go(route.module, tab)}
            />
          )}
        </div>
        <div className={css.content}>
          <Empty title="这里还是空的" description={`「${def.title}${tabTitle ? ` / ${tabTitle}` : ''}」的页面将在第 3 阶段接入。`} />
        </div>
      </main>
      <CommandPalette open={palette} onClose={() => setPalette(false)} onPick={(item) => go(item.module, item.tab)} />
      <ChangePasswordDialog open={password} onClose={() => setPassword(false)} />
    </div>
  )
}
