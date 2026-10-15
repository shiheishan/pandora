import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'

// ---------------------------------------------------------------------------
// 站点设置（GET/POST v1/settings/site）：站点名只有这一个来源（系统设置 site.name），
// 门户标题、入口页 <title>、邮件里的站点名与发件人名、订阅配置名都取它；主题只管标语与 Logo。
// 外框（Shell、侧栏）与「内容与外观」页共用这一份查询和缓存键：那边保存后失效，这边标题跟着变。
// ---------------------------------------------------------------------------
export const siteSettingsSchema = z.object({ name: z.string(), timezone: z.string() })
export type SiteSettings = z.output<typeof siteSettingsSchema>

/** 与 screens/content/queries.ts 的 [...CK, 'site'] 相同，内容页 invalidate('site') 会一并刷新外框 */
export const SITE_SETTINGS_KEY = ['admin', 'content', 'site'] as const

/** 与后端 settings 的缺省站点名一致 */
export const DEFAULT_SITE_NAME = 'Pandora'

/**
 * 面板下发入口页时把站点名写进 <meta name="application-name">（panel/internal/pkg/webapp 的 IndexMeta）；
 * 登录前拿不到接口，就用它。开发期 Vite 直接给源码入口，没有这个标签，回落 Pandora
 */
export function documentSiteName(doc: Pick<Document, 'querySelector'> = document): string {
  return doc.querySelector('meta[name="application-name"]')?.getAttribute('content')?.trim() || DEFAULT_SITE_NAME
}

export function useSiteSettings() {
  const api = useApi()
  return useQuery({ queryKey: SITE_SETTINGS_KEY, queryFn: ({ signal }) => api.get('v1/settings/site', siteSettingsSchema, { signal }) })
}

/** 已登录的外框用：接口回来以接口为准，回来之前用入口页里的值 */
export function useAdminSiteName(): string {
  return useSiteSettings().data?.name.trim() || documentSiteName()
}
