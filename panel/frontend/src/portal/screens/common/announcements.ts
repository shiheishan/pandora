/**
 * [INPUT]: 依赖 @tanstack/react-query 的 useQuery，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 announcementSchema、Announcement、AnnouncementSeverity、SEVERITY_LABEL、useAnnouncements
 * [POS]: portal/screens/common 的公告读模型（契约门户-08 GET v1/me/announcements）：概览公告卡与顶部 critical 横幅、消息页的公告标签共用同一查询与级别文案
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

export const announcementSchema = z.object({
  id: z.string(),
  title: z.string(),
  body: z.string(),
  severity: z.enum(['info', 'notice', 'warning', 'critical']),
  pinned: z.boolean(),
  // notify/announce.go 把 published_at 扫进 any，列表按 COALESCE(published_at, created_at) 排序，NULL 会编码成 null
  published_at: z.string().nullable(),
})
export type Announcement = z.output<typeof announcementSchema>
export type AnnouncementSeverity = Announcement['severity']

/** 级别色点的无障碍名称（契约门户-08：info 不显示，notice / warning / critical 分别用 --info / --warn / --danger） */
export const SEVERITY_LABEL: Readonly<Record<AnnouncementSeverity, string>> = { info: '', notice: '提示', warning: '注意', critical: '重要' }

const listSchema = z.object({ announcements: z.array(announcementSchema) })

/** 最多 20 条，置顶优先，服务端已按用户组与套餐定向过滤。 */
export function useAnnouncements() {
  const api = useApi()
  return useQuery({
    queryKey: ['portal', 'announcements'],
    queryFn: ({ signal }) => api.get('v1/me/announcements', listSchema, { signal }),
    select: (d) => d.announcements,
    meta: { topics: ['announcements.changed'] },
  })
}
