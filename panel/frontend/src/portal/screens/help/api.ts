import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { z } from 'zod'
import { useApi } from '../../../shell/runtime'

const CONTENT_KINDS = ['page', 'kb_article', 'tutorial', 'legal'] as const

// Go portalPageView：category / summary / published_at 都是 omitempty，空即缺席
export const contentPageSchema = z.object({
  slug: z.string(),
  kind: z.enum(CONTENT_KINDS),
  category: z.string().optional(),
  version: z.number().int(),
  title: z.string(),
  summary: z.string().optional(),
  published_at: z.string().optional(),
})
export type ContentPage = z.output<typeof contentPageSchema>

// 契约写 body: string，但 Go 同样是 omitempty：空正文时字段缺席，归一为 ''
export const contentDetailSchema = z.object({ page: contentPageSchema.extend({ body: z.string().optional().transform((b) => b ?? '') }) })

const PREFIX = ['portal', 'help'] as const

export function useHelpPages(q: string) {
  const api = useApi()
  return useQuery({
    queryKey: [...PREFIX, 'list', q],
    queryFn: ({ signal }) => api.get('v1/content/pages', z.object({ pages: z.array(contentPageSchema) }), { query: { q: q || undefined }, signal }),
    select: (d) => d.pages,
    // 边打字边搜：换关键词时先留着上一份结果，不闪骨架
    placeholderData: keepPreviousData,
  })
}

export function useHelpPage(slug: string | null) {
  const api = useApi()
  return useQuery({
    queryKey: [...PREFIX, 'page', slug ?? ''],
    queryFn: ({ signal }) => api.get(`v1/content/pages/${encodeURIComponent(slug!)}`, contentDetailSchema, { signal }),
    select: (d) => d.page,
    enabled: slug !== null,
  })
}
