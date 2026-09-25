/**
 * [INPUT]: 依赖 @tanstack/react-query 的 keepPreviousData / useMutation / useQuery，依赖 zod，依赖 ../../../shell/runtime 的 useApi
 * [OUTPUT]: 对外提供 CONTENT_KINDS、contentPageSchema / ContentPage、contentDetailSchema、HELP_QUERY、useHelpPages、useHelpPage、useHelpFeedback
 * [POS]: portal/screens/help 的数据层（契约门户-09，修订 R43）：文章列表（不带正文，q 由后端对标题 / 摘要 / 正文做包含匹配）、正文、「有帮助」反馈。三处都带同一组可见性参数 HELP_QUERY（platform=any 看全部平台的教程），反馈的可见性与读正文按同一套规则判定，参数不一致会 404
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { keepPreviousData, useMutation, useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

export const CONTENT_KINDS = ['page', 'kb_article', 'tutorial', 'legal'] as const

// Go contentPageResponse：category / summary / 客户端版本 / published_at 都是 omitempty，空即缺席
export const contentPageSchema = z.object({
  slug: z.string(),
  kind: z.enum(CONTENT_KINDS),
  category: z.string().optional(),
  version: z.number().int(),
  title: z.string(),
  summary: z.string().optional(),
  locale: z.string(),
  target_platforms: z.array(z.string()),
  min_client_version: z.string().optional(),
  max_client_version: z.string().optional(),
  published_at: z.string().optional(),
})
export type ContentPage = z.output<typeof contentPageSchema>

// 契约写 body: string，但 Go 同样是 omitempty：空正文时字段缺席，归一为 ''
export const contentDetailSchema = z.object({ page: contentPageSchema.extend({ body: z.string().optional().transform((b) => b ?? '') }) })

/** 可见性参数：帮助中心不按平台过滤；locale 用后端默认 zh-CN，client_version 不传（网页没有客户端版本） */
export const HELP_QUERY = { platform: 'any' } as const

const PREFIX = ['portal', 'help'] as const

export function useHelpPages(q: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...PREFIX, 'list', q],
    queryFn: ({ signal }) => api.get('v1/content/pages', z.object({ pages: z.array(contentPageSchema) }), { query: { ...HELP_QUERY, q: q || undefined }, signal }),
    select: (d) => d.pages,
    // 边打字边搜：换关键词时先留着上一份结果，不闪骨架
    placeholderData: keepPreviousData,
  })
}

export function useHelpPage(slug: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...PREFIX, 'page', slug ?? ''],
    queryFn: ({ signal }) => api.get(`v1/content/pages/${encodeURIComponent(slug!)}`, contentDetailSchema, { query: HELP_QUERY, signal }),
    select: (d) => d.page,
    enabled: slug !== null,
  })
}

/** 按用户、文章、版本 upsert，天然幂等，不带幂等键 */
export function useHelpFeedback() {
  const api = useApi()
  return useMutation({
    mutationFn: ({ slug, version, helpful }: { slug: string; version: number; helpful: boolean }) =>
      api.post(`v1/content/pages/${encodeURIComponent(slug)}/feedback`, z.object({ ok: z.literal(true) }), { query: HELP_QUERY, body: { helpful, version } }),
  })
}
