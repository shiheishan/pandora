/** 与后端 settings 的缺省站点名一致 */
export const DEFAULT_SITE_NAME = 'Pandora'

/**
 * 面板下发入口页时把站点名写进 <meta name="application-name">（panel/internal/pkg/webapp 的 IndexMeta）；
 * 登录前拿不到接口，就用它。开发期 Vite 直接给源码入口，没有这个标签，回落 Pandora
 */
export function documentSiteName(doc: Pick<Document, 'querySelector'> = document): string {
  return doc.querySelector('meta[name="application-name"]')?.getAttribute('content')?.trim() || DEFAULT_SITE_NAME
}
