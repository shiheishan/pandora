/**
 * [INPUT]: 依赖 ../types 的 MockModule / MockContext，依赖 ./billing 的 readStrict，依赖 ./fixtures 的 gate / scenario
 * [OUTPUT]: 对外提供 help 模块的假接口 MockModule
 * [POS]: dev/mock/portal 的「帮助中心（门户-09）」假接口，归门户前端；形状照 api-contract.md（修订 R43）与 Go api/public/content.go、domain/content：列表不带正文、按 published_at 倒序、q 对标题 / 摘要 / 正文不区分大小写包含匹配（> 100 字 422）、platform 为空时隐藏限定了平台的教程、any 不过滤；正文 slug 非法或不可见 404，空正文省略 body；反馈 helpful 必填（422 fields.helpful）、版本不存在 422 fields.version、可见性按同一组参数判定、多余字段 400。empty 场景没有文章
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { MockContext, MockModule } from '../types.ts'
import { readStrict } from './billing.ts'
import { gate, scenario } from './fixtures.ts'

interface Article {
  slug: string
  kind: 'page' | 'kb_article' | 'tutorial' | 'legal'
  category: string
  version: number
  title: string
  summary: string
  body: string
  target_platforms: string[]
  daysAgo: number
}

const PLATFORMS = new Set(['web', 'windows', 'macos', 'linux', 'android', 'ios'])
const SLUG = /^[a-z0-9]+(-[a-z0-9]+)*$/

const ARTICLES: readonly Article[] = [
  {
    slug: 'first-connection',
    kind: 'kb_article',
    category: '快速上手',
    version: 3,
    title: '三分钟完成首次连接',
    summary: '复制订阅地址或一键导入，测速后选择节点。',
    body: '在「我的订阅」页面复制订阅地址，或直接点击对应客户端的一键导入按钮。\n\n## 选择节点\n打开客户端后先测速，选择延迟最低的节点。\n倍率大于 1 的节点会按倍率计流量。\n\n## 遇到问题\n若连接失败，请先更新订阅，再尝试切换协议不同的节点。',
    target_platforms: [],
    daysAgo: 3,
  },
  {
    slug: 'clash-verge',
    kind: 'tutorial',
    category: '客户端教程',
    version: 1,
    title: 'Clash Verge Rev（Windows / macOS）',
    summary: '',
    body: '配置 → 新建 → 类型选择 Remote，粘贴订阅地址。\n\n## 常见问题\n提示解析失败：请确认复制的是完整的订阅地址，或使用「一键导入」。',
    target_platforms: ['windows', 'macos'],
    daysAgo: 22,
  },
  {
    slug: 'shadowrocket',
    kind: 'tutorial',
    category: '客户端教程',
    version: 2,
    title: 'Shadowrocket（iOS）',
    summary: '',
    body: '点击右上角 ＋，类型选择 Subscribe，粘贴订阅地址。',
    target_platforms: ['ios'],
    daysAgo: 41,
  },
  {
    slug: 'hiddify',
    kind: 'tutorial',
    category: '客户端教程',
    version: 1,
    title: 'Hiddify（Android）',
    summary: '',
    body: '打开 Hiddify，点击「新建配置 → 从剪贴板添加」。',
    target_platforms: ['android'],
    daysAgo: 9,
  },
  {
    slug: 'gift-cards',
    kind: 'kb_article',
    category: '账单与支付',
    version: 1,
    title: '如何使用礼品卡',
    summary: '',
    body: '在「钱包」页输入卡码，先查询内容再确认兑换。余额卡直接入账，流量卡进流量包余额，套餐卡立即延长订阅。',
    target_platforms: [],
    daysAgo: 56,
  },
  {
    slug: 'device-limit',
    kind: 'kb_article',
    category: '',
    version: 1,
    title: '同时在线设备数是怎么算的',
    summary: '按近 24 小时拉取订阅的来源计算。',
    body: '',
    target_platforms: [],
    daysAgo: 70,
  },
  { slug: 'terms', kind: 'legal', category: '条款', version: 4, title: '服务条款', summary: '', body: '（法律文本不进帮助中心）', target_platforms: [], daysAgo: 100 },
]

function publishedAt(a: Article): string {
  return new Date(Date.now() - a.daysAgo * 86_400_000).toISOString()
}

function view(a: Article, withBody: boolean) {
  return {
    slug: a.slug,
    kind: a.kind,
    ...(a.category ? { category: a.category } : {}),
    version: a.version,
    title: a.title,
    ...(a.summary ? { summary: a.summary } : {}),
    ...(withBody && a.body ? { body: a.body } : {}),
    locale: 'zh-CN',
    target_platforms: a.target_platforms,
    published_at: publishedAt(a),
  }
}

/** 可见性参数校验与过滤，照 domain/content visible()；非法参数写好 422 并返回 null */
function visibleArticles(ctx: MockContext): Article[] | null {
  const kind = ctx.query.get('kind') ?? ''
  if (kind && !['page', 'kb_article', 'tutorial', 'legal'].includes(kind)) {
    ctx.fail(422, 'validation_failed', '请求参数校验未通过', { kind: '无效的内容类型' })
    return null
  }
  const platform = (ctx.query.get('platform') ?? '').trim().toLowerCase()
  if (platform && platform !== 'any' && !PLATFORMS.has(platform)) {
    ctx.fail(422, 'validation_failed', '请求参数校验未通过', { platform: '无效的平台' })
    return null
  }
  const locale = ctx.query.get('locale')?.trim() || 'zh-CN'
  if (scenario() === 'empty' || locale !== 'zh-CN') return []
  return ARTICLES.filter(
    (a) => (!kind || a.kind === kind) && (platform === 'any' || a.target_platforms.length === 0 || (platform !== '' && a.target_platforms.includes(platform))),
  )
}

// 反馈：按用户、文章、版本 upsert（content_page_feedback 主键）
const feedback = new Map<string, boolean>()

export const help: MockModule = {
  routes: {
    'GET /v1/content/pages': async (ctx) => {
      if (!(await gate(ctx))) return
      const q = (ctx.query.get('q') ?? '').trim()
      if ([...q].length > 100) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { q: '搜索词最多 100 个字' })
      const list = visibleArticles(ctx)
      if (!list) return
      const needle = q.toLowerCase()
      const hits = list.filter((a) => !needle || `${a.title} ${a.summary} ${a.body}`.toLowerCase().includes(needle))
      hits.sort((a, b) => a.daysAgo - b.daysAgo)
      ctx.send(200, { pages: hits.slice(0, 200).map((a) => view(a, false)) })
    },
    'GET /v1/content/pages/:slug': async (ctx) => {
      if (!(await gate(ctx))) return
      const list = visibleArticles(ctx)
      if (!list) return
      const slug = ctx.params.slug!.trim().toLowerCase()
      const found = SLUG.test(slug) ? list.find((a) => a.slug === slug) : undefined
      if (!found) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      ctx.send(200, { page: view(found, true) })
    },
    'POST /v1/content/pages/:slug/feedback': async (ctx) => {
      const body = await readStrict(ctx, ['helpful', 'version'])
      if (!body) return
      if (typeof body.helpful !== 'boolean') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { helpful: '必填' })
      const list = visibleArticles(ctx)
      if (!list) return
      const slug = ctx.params.slug!.trim().toLowerCase()
      const found = SLUG.test(slug) ? list.find((a) => a.slug === slug) : undefined
      if (!found) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      // 假后端只留当前版本；旧版本在后端仍收反馈（修订 R43），这里不模拟版本历史
      if (body.version !== found.version) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { version: '文章版本不存在' })
      feedback.set(`${ctx.user.userId}\n${found.slug}\n${found.version}`, body.helpful)
      ctx.send(200, { ok: true })
    },
  },
}
