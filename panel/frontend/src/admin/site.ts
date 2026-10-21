import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../shell/runtime'
import { siteSettingsSchema } from './screens/content/schemas'
import { documentSiteName } from './site-name'

// ---------------------------------------------------------------------------
// 站点设置（GET/POST v1/settings/site）：站点名只有这一个来源（系统设置 site.name），
// 门户标题、入口页 <title>、邮件里的站点名与发件人名、订阅配置名都取它；主题只管标语。
// 外框（Shell、侧栏）与「内容与外观」页共用这一份查询和缓存键：那边保存后失效，这边标题跟着变。
// ---------------------------------------------------------------------------
export { siteSettingsSchema }
export type SiteSettings = z.output<typeof siteSettingsSchema>

/** 与 screens/content/queries.ts 的 [...CK, 'site'] 相同，内容页 invalidate('site') 会一并刷新外框 */
export const SITE_SETTINGS_KEY = ['admin', 'content', 'site'] as const

export function useSiteSettings() {
  const api = useApi()
  return useQuery({ queryKey: SITE_SETTINGS_KEY, queryFn: ({ signal }) => api.get('v1/settings/site', siteSettingsSchema, { signal }) })
}

/** 已登录的外框用：接口回来以接口为准，回来之前用入口页里的值 */
export function useAdminSiteName(): string {
  return useSiteSettings().data?.name.trim() || documentSiteName()
}
