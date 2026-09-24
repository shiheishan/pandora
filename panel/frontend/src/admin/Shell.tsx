/**
 * [INPUT]: 依赖 react 的 state / effect / memo，依赖 ../core/router 的 useHashLocation / navigate，依赖 ../shell/ScreenFrame，依赖 ../ui 的 Tabs / Empty / Button，依赖 ./Sidebar、./EventsCapsule、./CommandPalette、./ChangePasswordDialog、./me、./modules、./screens，依赖 ./Shell.module.css
 * [OUTPUT]: 对外提供 Shell
 * [POS]: admin 登录后的外框（管理后台.dc.html showApp）：左侧 Sidebar，右侧粘性顶栏（面包屑 + 实时事件）、页头（标题 + 有权限的标签页）与模块内容区；内容区按路由从 screens 登记表取懒加载页面，包在 ScreenFrame 里，缺读权限的地址显示「无权限或不存在」
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useMemo, useState } from 'react'
import { navigate, useHashLocation } from '../core/router'
import { NotFoundScreen, ScreenFallback, ScreenFrame } from '../shell/ScreenFrame'
import { Button, Empty, Tabs } from '../ui'
import { ChangePasswordDialog } from './ChangePasswordDialog'
import { CommandPalette } from './CommandPalette'
import { EventsCapsule } from './EventsCapsule'
import { useAdminMe } from './me'
import { MODULES, canRead, modulePath, resolveRoute, visibleTabs, type AdminRoute, type ModuleKey, type Permissions } from './modules'
import { SCREENS } from './screens'
import css from './Shell.module.css'
import { Sidebar } from './Sidebar'

export function Shell() {
  const location = useHashLocation()
  const me = useAdminMe()
  const perms = useMemo<Permissions | undefined>(() => (me.data ? new Set(me.data.permissions) : undefined), [me.data])
  const route = resolveRoute(location.path, perms)
  const def = MODULES[route.module]
  const [palette, setPalette] = useState(false)
  const [password, setPassword] = useState(false)

  // 未知模块、缺省标签一律改写成规范地址，刷新与分享都落在同一处；
  // 等 GET v1/me 回来再改写，缺省标签才能落在第一个有权限的标签上。
  // 模块认得时查询串（页面的筛选条件）跟着走，落回仪表盘时丢掉
  useEffect(() => {
    if (!perms || location.path === route.canonical) return
    const sameModule = location.path.split('/')[1] === route.module
    navigate(route.canonical, { replace: true, query: sameModule ? Object.fromEntries(location.query) : undefined })
  }, [perms, location, route.canonical, route.module])

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

  const go = (module: ModuleKey, tab?: string | null) => navigate(modulePath(module, tab, perms))
  const tabs = perms ? visibleTabs(route.module, perms) : []

  return (
    <div className={css.frame}>
      <Sidebar current={route.module} me={me.data} perms={perms} onOpenPalette={() => setPalette(true)} onChangePassword={() => setPassword(true)} />
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
          {me.isError ? (
            <Empty
              title="读取账号信息失败"
              description="暂时拿不到当前账号的权限，页面无法判断能否显示。"
              action={
                <Button variant="secondary" size="sm" onClick={() => void me.refetch()}>
                  重试
                </Button>
              }
            />
          ) : perms ? (
            <Content route={route} perms={perms} />
          ) : (
            <ScreenFallback />
          )}
        </div>
      </main>
      <CommandPalette open={palette} perms={perms} onClose={() => setPalette(false)} onPick={(item) => go(item.module, item.tab)} />
      <ChangePasswordDialog open={password} onClose={() => setPassword(false)} />
    </div>
  )
}

// 权限只看 GET v1/me；缺读权限与不存在同样处理，不透露模块是否存在
function Content({ route, perms }: { route: AdminRoute; perms: Permissions }) {
  if (!canRead(route.module, route.tab, perms)) return <NotFoundScreen />
  const Screen = SCREENS[route.module]
  return (
    <ScreenFrame resetKey={route.canonical}>
      <Screen tab={route.tab} rest={route.rest} />
    </ScreenFrame>
  )
}
