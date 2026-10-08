import { createContext, useContext, useEffect, type ReactNode } from 'react'

// ---------------------------------------------------------------------------
// 页头：标题默认取 pages.ts 的 PAGES；页面里的子页（加流量、换个套餐、确认、兑换…）经 usePageHead
// 换成自己的标题，并在标题上方给「‹ 返回」。返回优先退回应用内的上一页，直接打开的地址退到 parent。
// ---------------------------------------------------------------------------
export interface PageHead {
  title: string
  /** 返回的落点（应用内没有上一页时用），如 '/subs' */
  parent?: string
}

const HeadContext = createContext<(head: PageHead | null) => void>(() => {})

export function PageHeadProvider({ set, children }: { set: (head: PageHead | null) => void; children: ReactNode }) {
  return <HeadContext.Provider value={set}>{children}</HeadContext.Provider>
}

/** title 为 null 时用默认标题；离开页面自动还原 */
export function usePageHead(title: string | null, parent?: string) {
  const set = useContext(HeadContext)
  useEffect(() => {
    set(title === null ? null : { title, parent })
    return () => set(null)
  }, [title, parent, set])
}
