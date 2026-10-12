// 后台节点页基准：首屏、加载期长任务、静置期 nodes.changed 引起的重拉与 CPU、滚动帧率、全选与搜索响应。
// puppeteer-core 驱动本机 Chrome（无头），一次输出一个 JSON。
//
// 用法：node nodes-bench.mjs <后台地址|端口> [CPU 降速倍数=1] [静置观察秒数=20]
//   后台地址：本机 vite preview 写端口或 http://localhost:4711/；测试机写 https://panel.example.com/<后台前缀>/
// 环境变量：
//   ADMIN_TOKEN   直接用这个令牌（测试机上用总协调给的测试账号令牌，不落盘）；没有就用假后端账号登录
//   MOCK_EMAIL / MOCK_PASSWORD  假后端账号，默认 admin@pandora.dev / pandora-dev-pass（dev/mock-api.ts 的 MOCK_ACCOUNTS）
//   EXPECT_ROWS   「首屏」以多少行进入 DOM 为准，默认 990（1000 节点的假后端）；测试机按实际节点数改
//   CHROME        Chrome 可执行文件，默认 macOS 的 /Applications/Google Chrome.app
// 依赖页面上的 aria-label：「选择 <节点名>」「全选当前列表」「搜索节点」；改了这几个文案要同步改这里。
import puppeteer from 'puppeteer-core'

const [target, throttle = '1', observeSec = '20'] = process.argv.slice(2)
if (!target) { console.error('用法: node nodes-bench.mjs <后台地址|端口> [降速] [观察秒数]'); process.exit(2) }
const base = /^\d+$/.test(target) ? `http://localhost:${target}/` : target.replace(/\/?$/, '/')
const expectRows = Number(process.env.EXPECT_ROWS || 990)
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

let token = process.env.ADMIN_TOKEN
if (!token) {
  const login = await fetch(new URL('v1/auth/login', base), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ email: process.env.MOCK_EMAIL || 'admin@pandora.dev', password: process.env.MOCK_PASSWORD || 'pandora-dev-pass' }) }).then((r) => r.json())
  token = login.access_token
  if (!token) { console.error('登录失败：', JSON.stringify(login)); process.exit(1) }
}
const browser = await puppeteer.launch({ executablePath: chrome, headless: true, args: ['--no-first-run'] })
const page = await browser.newPage()
await page.setViewport({ width: 1440, height: 900 })
const cdp = await page.createCDPSession()
await cdp.send('Emulation.setCPUThrottlingRate', { rate: Number(throttle) })
await page.evaluateOnNewDocument((tok, expect) => {
  localStorage.setItem('pandora-admin-token', tok)
  window.__lt = []
  new PerformanceObserver((l) => { for (const e of l.getEntries()) window.__lt.push({ s: e.startTime, d: e.duration }) }).observe({ type: 'longtask', buffered: true })
  window.__rowsAt = null
  const mo = new MutationObserver(() => {
    if (window.__rowsAt === null && document.querySelectorAll('[aria-label^="选择 "]').length >= expect) window.__rowsAt = performance.now()
  })
  document.addEventListener('DOMContentLoaded', () => mo.observe(document.body, { childList: true, subtree: true }))
  // 数 SSE 里的 nodes.changed：事件流经 fetch 读，解码后的文本里数帧头
  window.__nc = 0
  const dec = TextDecoder.prototype.decode
  TextDecoder.prototype.decode = function (...a) { const out = dec.apply(this, a); if (typeof out === 'string' && out.includes('event: nodes.changed')) window.__nc += out.split('event: nodes.changed').length - 1; return out }
}, token, expectRows)
const nodeReqs = []
page.on('response', async (r) => { if (r.url().includes('/v1/nodes?')) { let len = 0; try { len = (await r.buffer()).length } catch { /* 导航中断 */ } nodeReqs.push({ t: Date.now(), url: r.url().replace(base, ''), len }) } })
const otherRefetch = []
page.on('request', (r) => { const u = r.url(); if (/\/v1\/(servers|node-pools|route-groups|dashboard\/tasks|overview)/.test(u)) otherRefetch.push(u.replace(base, '')) })
await page.goto(`${base}#/nodes/nodes`, { waitUntil: 'load' })
await page.waitForFunction(() => document.querySelectorAll('[aria-label^="选择 "]').length >= 10, { timeout: 60000 })
await new Promise((r) => setTimeout(r, 1500))
const load = await page.evaluate(() => {
  const fcp = performance.getEntriesByName('first-contentful-paint')[0]?.startTime
  const rows = document.querySelectorAll('[aria-label^="选择 "]').length
  const lt = window.__lt.filter((e) => e.s < (window.__rowsAt ?? performance.now()))
  return { fcp, rowsAt: window.__rowsAt, rows, dom: document.getElementsByTagName('*').length, loadLongTasks: lt.length, loadLongMax: Math.max(0, ...lt.map((e) => e.d)), loadTBT: lt.reduce((a, e) => a + Math.max(0, e.d - 50), 0), heapMB: performance.memory ? performance.memory.usedJSHeapSize / 1048576 : null }
})
load.listBytes = nodeReqs.length ? nodeReqs[0].len : null
load.listUrl = nodeReqs.length ? nodeReqs[0].url : null
// 静置观察：nodes.changed 引起的重拉与长任务
await cdp.send('Performance.enable')
const metrics = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value]))
const m0 = await metrics()
const t0 = await page.evaluate(() => performance.now())
const nc0 = await page.evaluate(() => window.__nc)
const reqBefore = nodeReqs.length; const otherBefore = otherRefetch.length
await new Promise((r) => setTimeout(r, Number(observeSec) * 1000))
const idle = await page.evaluate((t0) => {
  const now = performance.now(); const lt = window.__lt.filter((e) => e.s >= t0)
  return { windowMs: now - t0, longTasks: lt.length, longMax: Math.max(0, ...lt.map((e) => e.d)), longAvg: lt.length ? lt.reduce((a, e) => a + e.d, 0) / lt.length : 0, busyMs: lt.reduce((a, e) => a + e.d, 0), tbt: lt.reduce((a, e) => a + Math.max(0, e.d - 50), 0) }
}, t0)
const m1 = await metrics()
for (const k of ['TaskDuration', 'ScriptDuration', 'LayoutDuration', 'RecalcStyleDuration']) idle['cpu_' + k] = (m1[k] - m0[k]) * 1000
idle.nodeRefetches = nodeReqs.length - reqBefore
idle.nodesChanged = (await page.evaluate(() => window.__nc)) - nc0
idle.scriptPerRefetch = idle.nodeRefetches ? idle.cpu_ScriptDuration / idle.nodeRefetches : null
idle.taskPerRefetch = idle.nodeRefetches ? idle.cpu_TaskDuration / idle.nodeRefetches : null
idle.otherRefetches = otherRefetch.slice(otherBefore).reduce((m, u) => { const k = u.split('?')[0]; m[k] = (m[k] || 0) + 1; return m }, {})
// 滚动帧率：每帧滚 30px，持续 6 秒
const scroll = await page.evaluate(() => new Promise((resolve) => {
  const cands = [document.scrollingElement, ...document.querySelectorAll('*')].filter((e) => e && e.scrollHeight > e.clientHeight + 200 && (e === document.scrollingElement || /auto|scroll/.test(getComputedStyle(e).overflowY)))
  const el = cands.sort((a, b) => b.scrollHeight - a.scrollHeight)[0]
  const frames = []; let last = performance.now(); const start = last
  const step = (t) => {
    frames.push(t - last); last = t; el.scrollTop += 30; if (el.scrollTop + el.clientHeight >= el.scrollHeight - 2) el.scrollTop = 0
    if (t - start < 6000) { requestAnimationFrame(step); return }
    frames.shift(); const s = [...frames].sort((a, b) => a - b)
    resolve({ el: el === document.scrollingElement ? 'document' : el.className.slice(0, 40), scrollH: el.scrollHeight, frames: frames.length, fps: frames.length / ((t - start) / 1000), p50: s[Math.floor(s.length * 0.5)], p95: s[Math.floor(s.length * 0.95)], max: s[s.length - 1], janky: frames.filter((f) => f > 50).length })
  }
  requestAnimationFrame(step)
}))
// 全选：点击到下一帧
const selectAll = await page.evaluate(() => new Promise((resolve) => {
  const cb = document.querySelector('[aria-label="全选当前列表"]'); if (!cb) { resolve(null); return }
  const t = performance.now(); cb.click()
  requestAnimationFrame(() => setTimeout(() => resolve(performance.now() - t), 0))
}))
// 搜索框输入：到下一帧（服务端搜索时这里只量到发出请求前的那一帧）
const search = await page.evaluate(() => new Promise((resolve) => {
  const el = document.querySelector('[aria-label="搜索节点"]'); if (!el) { resolve(null); return }
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set
  const t = performance.now(); setter.call(el, '节点 05'); el.dispatchEvent(new Event('input', { bubbles: true }))
  requestAnimationFrame(() => setTimeout(() => resolve({ ms: performance.now() - t, rows: document.querySelectorAll('[aria-label^="选择 "]').length }), 0))
}))
console.log(JSON.stringify({ base, throttle, load, idle, scroll, selectAllMs: selectAll, search }, (k, v) => typeof v === 'number' ? Math.round(v * 10) / 10 : v, 1))
await browser.close()
