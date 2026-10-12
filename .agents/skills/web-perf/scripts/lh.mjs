// Lighthouse 跑一次导航（user flow，可先往 localStorage 写令牌再导航），输出一行 JSON 指标。
//
// 用法：node lh.mjs <URL> <desktop|mobile> <cold|warm> [HTML 报告路径]
//   cold：Lighthouse 默认清存储与 HTTP 缓存后再导航（首次访问）
//   warm：先用 puppeteer 走一遍把缓存填上，正式那次 disableStorageReset（回访）
//   mobile 是 Lighthouse 默认预设（模拟 4 倍 CPU 降速、慢 4G），desktop 用 desktopConfig
// 环境变量：
//   TOKEN       令牌；有就在每个新文档开始时写进 localStorage（冷缓存清掉存储后也会重新写入）。测试账号令牌由总协调给，不落盘
//   TOKEN_KEY   键名，门户 pandora-portal-token（默认），后台 pandora-admin-token
//   CHROME      Chrome 可执行文件，默认 macOS 的 /Applications/Google Chrome.app
import fs from 'node:fs'
import puppeteer from 'puppeteer-core'
import { desktopConfig, startFlow } from 'lighthouse'

const [url, preset = 'desktop', cache = 'cold', htmlOut] = process.argv.slice(2)
if (!url || !['desktop', 'mobile'].includes(preset) || !['cold', 'warm'].includes(cache)) {
  console.error('用法: node lh.mjs <URL> <desktop|mobile> <cold|warm> [HTML 报告路径]'); process.exit(2)
}
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const browser = await puppeteer.launch({ executablePath: chrome, headless: true, args: ['--no-first-run'] })
try {
  const page = await browser.newPage()
  if (process.env.TOKEN) {
    await page.evaluateOnNewDocument((k, t) => localStorage.setItem(k, t), process.env.TOKEN_KEY || 'pandora-portal-token', process.env.TOKEN)
  }
  const flow = await startFlow(page, { config: preset === 'desktop' ? desktopConfig : undefined })
  if (cache === 'warm') {
    // 不能等 networkidle：SSE（v1/events）长连接一直开着，网络永远不空闲
    await page.goto(url, { waitUntil: 'load' })
    await new Promise((r) => setTimeout(r, 3000))
    // 先离开再回来：URL 相同（只差 #路由）时是同文档跳转，Lighthouse 量不到绘制（NO_FCP）
    await page.goto('about:blank')
    await flow.navigate(url, { disableStorageReset: true })
  } else {
    await flow.navigate(url)
  }
  const result = await flow.createFlowResult()
  const lhr = result.steps[0].lhr
  if (htmlOut) fs.writeFileSync(htmlOut, await flow.generateReport())
  const a = lhr.audits
  const num = (id) => (a[id] && typeof a[id].numericValue === 'number' ? Math.round(a[id].numericValue * 1000) / 1000 : null)
  const reqs = a['network-requests']?.details?.items ?? []
  const origin = new URL(url).origin
  // 各接口耗时（毫秒）：请求发出到响应结束；同一路径多次请求取最慢那次
  const api = {}
  for (const r of reqs) {
    const u = new URL(r.url)
    if (u.origin !== origin || !/\/v1\//.test(u.pathname)) continue
    const k = u.pathname.replace(/^.*\/v1\//, 'v1/').replace(/[0-9a-f]{8}-[0-9a-f-]{27}/g, '{id}')
    const ms = Math.round((r.networkEndTime ?? 0) - (r.networkRequestTime ?? r.rendererStartTime ?? 0))
    api[k] = Math.max(api[k] ?? 0, ms)
  }
  const fromOrigin = reqs.filter((r) => r.url.startsWith(origin))
  console.log(JSON.stringify({
    url, preset, cache,
    score: lhr.categories.performance?.score ?? null,
    ttfb: num('server-response-time'),
    fcp: num('first-contentful-paint'),
    lcp: num('largest-contentful-paint'),
    tbt: num('total-blocking-time'),
    cls: num('cumulative-layout-shift'),
    si: num('speed-index'),
    requests: reqs.length,
    transferKB: Math.round(reqs.reduce((s, r) => s + (r.transferSize || 0), 0) / 102.4) / 10,
    protocols: [...new Set(fromOrigin.map((r) => r.protocol))].join(','),
    api,
    warnings: lhr.runWarnings,
  }))
} finally {
  await browser.close()
}
