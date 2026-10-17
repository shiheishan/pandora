import { createContext, useContext, useEffect, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

// ---------------------------------------------------------------------------
// 页头：标题默认取 pages.ts 的 PAGES；页面里的子页（加流量、换个套餐、确认、兑换…）经 usePageHead
// 换成自己的标题，并在标题上方给「‹ 返回」。返回优先退回应用内的上一页，直接打开的地址退到 parent。
// 页头右侧的控件（如选购页的「按时间 / 一次性」）用 PageHeadAside 放进去，不要经 usePageHead 传 ReactNode（每次渲染都是新对象，会反复触发 effect）。
// ---------------------------------------------------------------------------
export interface PageHead {
  title: string
  /** 返回的落点（应用内没有上一页时用），如 '/subs' */
  parent?: string
}

const HeadContext = createContext<(head: PageHead | null) => void>(() => {})
const AsideContext = createContext<HTMLElement | null>(null)

export function PageHeadProvider({
  set,
  aside,
  children,
}: {
  set: (head: PageHead | null) => void
  aside: HTMLElement | null
  children: ReactNode
}) {
  return (
    <HeadContext.Provider value={set}>
      <AsideContext.Provider value={aside}>{children}</AsideContext.Provider>
    </HeadContext.Provider>
  )
}

/** title 为 null 时用默认标题；离开页面自动还原 */
export function usePageHead(title: string | null, parent?: string) {
  const set = useContext(HeadContext)
  useEffect(() => {
    set(title === null ? null : { title, parent })
    return () => set(null)
  }, [title, parent, set])
}

export function PageHeadAside({ children }: { children: ReactNode }) {
  const slot = useContext(AsideContext)
  if (!slot) return null
  return createPortal(children, slot)
}

