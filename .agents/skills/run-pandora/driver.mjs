#!/usr/bin/env node
// 驱动面板前端（假后端）：起 vite、登录后台和门户、逐页截图、读页面文字。
// 用法：node .agents/skills/run-pandora/driver.mjs [shots|text <admin|portal> <hash>] [--out DIR]
//   shots              起两个入口，登录，各截几页（缺省）
//   text <app> <hash>  登录后打开 #hash，打印页面可见文字
// 已有 5181/5182 在跑就复用，不会关别人起的进程。
import { spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { mkdirSync } from 'node:fs'
import { resolve, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import net from 'node:net'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const fe = resolve(root, 'panel/frontend')
const { chromium } = createRequire(resolve(fe, 'package.json'))('@playwright/test')

const args = process.argv.slice(2)
const oi = args.indexOf('--out')
const out = resolve(oi >= 0 ? args.splice(oi, 2)[1] : '/tmp/pandora-run')
mkdirSync(out, { recursive: true })
const cmd = args[0] ?? 'shots'

const APPS = {
  admin: { port: 5182, mode: 'dev:admin', email: 'admin@pandora.dev', pages: ['', '#/users', '#/nodes'] },
  portal: { port: 5181, mode: 'dev:portal', email: 'user@pandora.dev', pages: ['', '#/plans'] },
}
const PASS = 'pandora-dev-pass'

const listening = (port) => new Promise((ok) => {
  const s = net.connect(port, 'localhost')
  s.once('connect', () => { s.destroy(); ok(true) })
  s.once('error', () => ok(false))
})

const started = []
async function ensureUp(name) {
  const a = APPS[name]
  if (await listening(a.port)) return
  const p = spawn('npm', ['run', a.mode, '--', '--port', String(a.port), '--strictPort'], { cwd: fe, stdio: 'ignore' })
  started.push(p)
  for (let i = 0; i < 60 && !(await listening(a.port)); i++) await new Promise((r) => setTimeout(r, 500))
  if (!(await listening(a.port))) throw new Error(`${name} vite 30 秒内没起来`)
}

async function login(browser, name) {
  const a = APPS[name]
  const ctx = await browser.newContext({ baseURL: `http://localhost:${a.port}`, viewport: { width: 1280, height: 900 }, locale: 'zh-CN' })
  const page = await ctx.newPage()
  await page.goto('/')
  await page.getByLabel('邮箱').fill(a.email)
  await page.getByLabel('密码', { exact: true }).fill(PASS)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await page.getByLabel('邮箱').waitFor({ state: 'hidden' })
  return page
}

const browser = await chromium.launch()
try {
  const names = cmd === 'text' ? [args[1]] : Object.keys(APPS)
  for (const n of names) await ensureUp(n)
  for (const n of names) {
    const page = await login(browser, n)
    const pages = cmd === 'text' ? [args[2] ?? ''] : APPS[n].pages
    for (const h of pages) {
      await page.goto(`/${h}`)
      await page.waitForLoadState('load'); await page.waitForTimeout(800)
      if (cmd === 'text') console.log(await page.locator('body').innerText())
      else {
        const f = `${out}/${n}${h.replace(/[^a-z]/g, '-') || '-home'}.png`
        await page.screenshot({ path: f })
        console.log('shot', f)
      }
    }
  }
} finally {
  await browser.close()
  for (const p of started) p.kill()
}
