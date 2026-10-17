import { IconButton } from '../ui'
import { EventsTrigger } from './EventsCapsule'
import { MENU_ICON } from './nav-icons'
import css from './Topbar.module.css'

export interface TopbarProps {
  /** 当前模块名，显示在中间 */
  title: string
  /** 抽屉是否打开（汉堡按钮的 aria-expanded） */
  navOpen: boolean
  onOpenNav: () => void
  /** 页面主按钮的插槽：窄屏时页面经 kit 的 PageActions 把按钮渲染到这里 */
  actionsRef?: (el: HTMLDivElement | null) => void
}

export function Topbar(p: TopbarProps) {
  const { title, navOpen, onOpenNav, actionsRef } = p

  return (
    <header className={css.top}>
      <IconButton
        label="打开导航"
        icon={MENU_ICON}
        onClick={onOpenNav}
        aria-controls="admin-sidebar"
        aria-expanded={navOpen}
      />
      <b className={css.title}>{title}</b>
      <div ref={actionsRef} className={css.actions} />
      <EventsTrigger variant="top" />
    </header>
  )
}
