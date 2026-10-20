// ---------------------------------------------------------------------------
// 服务端首页片段：门户入口页由面板在 <div id="root"> 之前输出一段首页（站点名、介绍、套餐、常见问题），
// 模板在 panel/internal/pkg/webapp/home.go，样式在 home.css。片段与 React 树是兄弟节点，React 只管它显不显：
// <html data-home="off"> 时藏起来（core/theme-boot.js 首帧前按令牌与地址先打一次，App 挂载后按登录状态与地址再定）。
// 本地 vite dev 与首页开关关着时没有片段，门户照旧直接给登录页。
// ---------------------------------------------------------------------------
export const HOME_ELEMENT_ID = 'ph-home'
export const HOME_ATTR = 'data-home'

/** 页面里有没有服务端输出的首页片段 */
export function hasServerHome(doc: Pick<Document, 'getElementById'> = document): boolean {
  return doc.getElementById(HOME_ELEMENT_ID) !== null
}

/** 藏起或露出首页片段 */
export function setHomeHidden(hidden: boolean, root: Pick<Element, 'setAttribute' | 'removeAttribute'> = document.documentElement): void {
  if (hidden) root.setAttribute(HOME_ATTR, 'off')
  else root.removeAttribute(HOME_ATTR)
}
