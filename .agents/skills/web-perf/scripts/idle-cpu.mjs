// 空闲主线程占用：页面静置 10 秒，CDP Performance 指标里 Task / Script / Style / Layout 各涨了多少毫秒。
// 用来抓常驻动画（如事件胶囊的呼吸灯）吃主线程：同一页面分别跑「动画开」和「动画关」对照。
//
// 用法：node idle-cpu.mjs <后台地址|端口> <关动画 0|1> [路由=/nodes/nodes]
// 环境变量：ADMIN_TOKEN、MOCK_EMAIL、MOCK_PASSWORD、CHROME 同 nodes-bench.mjs；
//   KILL_CSS 关动画时注入的样式，默认 `*{animation:none!important}`；只想关某一个，就写它的选择器
// 无头 Chrome 没有 GPU 合成，数字比有 GPU 的真浏览器偏大，只用来做开关对照。
import puppeteer from 'puppeteer-core'

const [target, kill = '0', route = '/nodes/nodes'] = process.argv.slice(2)
if (!target) { console.error('用法: node idle-cpu.mjs <后台地址|端口> <关动画 0|1> [路由]'); process.exit(2) }
const base = /^\d+$/.test(target) ? `http://localhost:${target}/` : target.replace(/\/?$/, '/')
const chrome = process.env.CHROME || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
let token = process.env.ADMIN_TOKEN
if (!token) {
  const login = await fetch(new URL('v1/auth/login', base), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ email: process.env.MOCK_EMAIL || 'admin@pandora.dev', password: process.env.MOCK_PASSWORD || 'pandora-dev-pass' }) }).then((r) => r.json())
  token = login.access_token
  if (!token) { console.error('登录失败：', JSON.stringify(login)); process.exit(1) }
}
const browser = await puppeteer.launch({ executablePath: chrome, headless: true, args: ['--no-first-run'] })
const page = await browser.newPage(); await page.setViewport({ width: 1440, height: 900 })
const cdp = await page.createCDPSession()
await page.evaluateOnNewDocument((tok) => localStorage.setItem('pandora-admin-token', tok), token)
await page.goto(`${base}#${route}`, { waitUntil: 'load' }); await new Promise((r) => setTimeout(r, 3000))
if (kill === '1') await page.addStyleTag({ content: process.env.KILL_CSS || '*{animation:none!important}' })
await cdp.send('Performance.enable')
const g = async () => Object.fromEntries((await cdp.send('Performance.getMetrics')).metrics.map((m) => [m.name, m.value]))
const a = await g(); await new Promise((r) => setTimeout(r, 10000)); const b = await g()
const dom = await page.evaluate(() => document.getElementsByTagName('*').length)
const ms = (k) => Math.round((b[k] - a[k]) * 1000)
console.log(JSON.stringify({ base, route, animOff: kill === '1', dom, per10s: { task: ms('TaskDuration'), script: ms('ScriptDuration'), style: ms('RecalcStyleDuration'), layout: ms('LayoutDuration') } }))
await browser.close()
