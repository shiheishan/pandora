/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery / useQueryClient，依赖 react 的 useCallback，依赖 ../../../shell/runtime 的 useApi，依赖 ../../actions 的 useCan / useFailure / useIntentKey（转出），依赖 ./schemas
 * [OUTPUT]: 对外提供 CK 查询键前缀、内容与外观页各读 hook（公告、知识库列表与单版本、套餐目录、主题、插槽、站点时区）、useInvalidateContent，并转出 useCan / useFailure / useIntentKey
 * [POS]: admin/screens/content 的数据层：读只经 react-query + core/api；公告挂 announcements.changed（门户可见内容的唯一表变更通知），其余没有通知的接口写后按 CK 前缀整体失效
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useApi } from '../../../shell/runtime'
import type { Kind } from './schemas'
import { announcementsResponse, pageResponse, pagesResponse, planCatalogResponse, siteSettingsSchema, slotsResponse, themesResponse } from './schemas'

export { useCan, useFailure, useIntentKey } from '../../actions'

export const CK = ['admin', 'content'] as const

export function useAnnouncements() {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'announcements'],
    queryFn: ({ signal }) => api.get('v1/announcements', announcementsResponse, { signal }),
    meta: { topics: ['announcements.changed'] },
  })
}

/**
 * 某类内容的全部版本（每个版本一行）：一次取到后端上限 500 条，搜索在前端做——
 * 后端 q 只按行匹配，标题改过的文章会只命中旧版本，按 slug 聚合时就拿错了「最新版」
 */
export function useContentPages(kind: Kind) {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'pages', kind],
    queryFn: ({ signal }) => api.get('v1/content-pages', pagesResponse, { signal, query: { kind, limit: 500 } }).then((r) => r.pages),
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[3] === kind ? prev : undefined),
  })
}

/**
 * 单个版本（含正文）；版本一旦写下就不再变，归档只改状态，所以可以长缓存。
 * 同一篇文章出了新版本时先留着上一版的数据，编辑区不因重新加载而卸载、丢掉输入；换了文章则不留
 */
export function useContentPage(id: string, slug: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'page', id],
    queryFn: ({ signal }) => api.get(`v1/content-pages/${id}`, pageResponse, { signal }).then((r) => r.page),
    staleTime: 60_000,
    placeholderData: (prev) => (prev?.slug === slug ? prev : undefined),
  })
}

/** 知识库「限定套餐」的名字：只在有 catalog.read 时请求 */
export function usePlanCatalog(enabled: boolean) {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'plans'],
    queryFn: ({ signal }) => api.get('v1/plans', planCatalogResponse, { signal }).then((r) => r.plans),
    enabled,
    staleTime: 5 * 60_000,
    meta: { topics: ['plans.changed'] },
  })
}

export function useThemes() {
  const api = useApi()
  return useQuery({
    queryKey: [...CK, 'themes'],
    queryFn: ({ signal }) => api.get('v1/themes', themesResponse, { signal }).then((r) => r.themes ?? []),
  })
}

export function useSlots() {
  const api = useApi()
  return useQuery({ queryKey: [...CK, 'slots'], queryFn: ({ signal }) => api.get('v1/slots', slotsResponse, { signal }).then((r) => r.slots) })
}

/** 站点时区：读权限与邮件设置相同（security.audit.read），没有就不请求、不画卡片 */
export function useSiteSettings(enabled: boolean) {
  const api = useApi()
  return useQuery({ queryKey: [...CK, 'site'], queryFn: ({ signal }) => api.get('v1/settings/site', siteSettingsSchema, { signal }), enabled })
}

export function useInvalidateContent() {
  const client = useQueryClient()
  return useCallback((...scope: string[]) => client.invalidateQueries({ queryKey: [...CK, ...scope] }), [client])
}
