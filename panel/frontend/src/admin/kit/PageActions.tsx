import { createContext, useContext, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import css from './PageActions.module.css'

// ---------------------------------------------------------------------------
// 页头插槽：外框画页头（标题 + 计数 + 帮助 + 操作），页面经 portal 把自己的按钮与计数放进去。
// 做法照门户的 PageHeadAside：外框把插槽元素经 context 交下来，页面用 createPortal 填。
// - 按钮可随页签变：页面按当前页签决定渲染哪些 <PageActions>。
// - 手机（≤639）外框把 actions 插槽换成手机顶栏右侧那格，页面不用管。
// - 详情模式、全幅模式、仪表盘没有外框页头，插槽是 null，PageActions / PageCount 什么都不画。
// ---------------------------------------------------------------------------

export interface PageActionsSlots {
  /** 页头右侧操作格（手机上是顶栏右侧） */
  actions: HTMLElement | null
  /** 标题后的计数格 */
  count: HTMLElement | null
}

const SlotsContext = createContext<PageActionsSlots>({ actions: null, count: null })

/** 外框用：把当前的插槽元素交给页面 */
export function PageActionsProvider({ slots, children }: { slots: PageActionsSlots; children: ReactNode }) {
  return <SlotsContext.Provider value={slots}>{children}</SlotsContext.Provider>
}

/** 页面用：把主按钮放进页头右侧（一般是一个 Button variant="primary"，最多再加一个次按钮） */
export function PageActions({ children }: { children: ReactNode }) {
  const { actions } = useContext(SlotsContext)
  if (!actions) return null
  return createPortal(children, actions)
}

/** 页面用：标题后的等宽计数小胶囊（样稿 .ph .cnt），如用户总数；value 为 null 时不画 */
export function PageCount({ value }: { value: number | string | null | undefined }) {
  const { count } = useContext(SlotsContext)
  if (!count || value == null) return null
  const text = typeof value === 'number' ? value.toLocaleString('zh-CN') : value
  return createPortal(<span className={css.count}>{text}</span>, count)
}
