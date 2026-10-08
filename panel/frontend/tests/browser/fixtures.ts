import { test as base, expect, type Browser, type BrowserContext, type Locator, type Page } from '@playwright/test'
import { appendFileSync } from 'node:fs'
import { join } from 'node:path'
import { epayNotify, exclusive, freshIp, type User } from './api.ts'
import { ADM, ADMIN_EMAIL, ADMIN_PASSWORD, PUB, SHOTS, STEPS_FILE } from './env.ts'
import { loadWorld, type World } from './seed.ts'

// ============================================================================
//  浏览器侧的公共件：登录、按步记录（文字 + 截图）、扮演收银台付款。
//  选择器只用角色与可见文字（getByRole / getByText / getByLabel），不给组件加 data-testid
// ============================================================================

export { expect }

export const test = base.extend<object, { world: World }>({
  // Playwright 的夹具约定：第一个参数必须是解构写法，这个夹具不依赖别的夹具
  // eslint-disable-next-line no-empty-pattern
  world: [async ({}, use) => use(loadWorld()), { scope: 'worker' }],
})

const WIDE = { width: 1280, height: 900 }
const NARROW = { width: 375, height: 812 }

async function newContext(browser: Browser, baseURL: string): Promise<BrowserContext> {
  return browser.newContext({
    baseURL,
    viewport: WIDE,
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
    extraHTTPHeaders: { 'X-Real-IP': freshIp() },
  })
}

/** 门户：在登录页填邮箱与口令，落到「我的套餐」 */
export async function openPortal(browser: Browser, u: User): Promise<Page> {
  const page = await (await newContext(browser, PUB)).newPage()
  await page.goto('/')
  await page.getByLabel('邮箱').fill(u.email)
  await page.getByLabel('密码', { exact: true }).fill(u.password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('navigation', { name: '主导航' })).toBeVisible()
  return page
}

/** 后台：同样从登录页进；写操作要重新认证时自动在弹框里填口令（登录自带 15 分钟窗口，通常不出现） */
export async function openAdmin(browser: Browser): Promise<Page> {
  const page = await (await newContext(browser, ADM)).newPage()
  await page.goto('/')
  await page.getByLabel('邮箱').fill(ADMIN_EMAIL)
  await page.getByLabel('密码', { exact: true }).fill(ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  const reauth = page.getByRole('dialog', { name: '验证身份后继续' })
  await page.addLocatorHandler(reauth, async () => {
    await reauth.getByLabel('当前登录密码').fill(ADMIN_PASSWORD)
    await reauth.getByRole('button').last().click()
    await expect(reauth).toBeHidden()
  })
  await expect(page.getByLabel('邮箱')).toBeHidden()
  return page
}

/** 门户按导航与地址前往：页面之间的约定地址见 .claude/rules/screens-portal.md */
export async function go(page: Page, hash: string): Promise<void> {
  await page.goto(`/#${hash}`)
}

// ----------------------------------------------------------------------------
//  按步记录：每步换一个来源地址、跑完截一张 1280 宽的图，结果写进 steps.jsonl（table.ts 出表）。
//  body 返回「页面上看到的」那句文字；断言失败时记失败与报错首行、截图后原样抛出
// ----------------------------------------------------------------------------
export interface StepOptions {
  /** 关键页面另截一张 375 宽 */
  narrow?: boolean
}

export async function step(page: Page, id: string, body: () => Promise<string>, opts: StepOptions = {}): Promise<void> {
  await page.context().setExtraHTTPHeaders({ 'X-Real-IP': freshIp() })
  const started = Date.now()
  const record = (ok: boolean, seen: string, shot: string) =>
    appendFileSync(STEPS_FILE, JSON.stringify({ id, ok, seen, ms: Date.now() - started, shot, test: base.info().title }) + '\n')
  try {
    const seen = await base.step(id, body)
    const shot = `${id}-1280.png`
    await page.screenshot({ path: join(SHOTS, shot), fullPage: true })
    if (opts.narrow) await narrowShot(page, id)
    record(true, oneLine(seen), shot)
  } catch (e) {
    const shot = `${id}-fail.png`
    await page.screenshot({ path: join(SHOTS, shot), fullPage: true }).catch(() => undefined)
    record(false, oneLine(e instanceof Error ? e.message : String(e)), shot)
    throw e
  }
}

/** 375 宽一张（手机），截完回到 1280 */
export async function narrowShot(page: Page, id: string): Promise<void> {
  await page.setViewportSize(NARROW)
  await page.screenshot({ path: join(SHOTS, `${id}-375.png`), fullPage: true })
  await page.setViewportSize(WIDE)
}

// 报错里 expect 带的终端颜色码（ESC [ … m）
const ANSI = new RegExp(`${String.fromCharCode(27)}\\[[0-9;]*m`, 'g')
const oneLine = (s: string) =>
  s
    .replace(ANSI, '')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .slice(0, 3)
    .join(' / ')
    .slice(0, 300)

/** 元素的可见文字，压成一行 */
export async function text(l: Locator): Promise<string> {
  return (await l.innerText()).replace(/\s+/g, ' ').trim()
}

// ----------------------------------------------------------------------------
//  付款：在付款页（或订单页的支付弹窗）读收银台地址，核对金额，以收银台身份送签名回调。
//  页面每 3 秒查一次订单，付完自己走到完成页——测试不替它跳
// ----------------------------------------------------------------------------
export async function payByCashier(scope: Page | Locator, world: World, amount: string): Promise<string> {
  const link = scope.getByRole('link', { name: /在这台电脑上付款|付款页/ })
  await expect(link).toBeVisible({ timeout: 15_000 })
  const href = await link.getAttribute('href')
  if (!href) throw new Error('付款链接没有地址')
  const q = new URL(href).searchParams
  const money = q.get('money') ?? ''
  const orderNo = q.get('out_trade_no') ?? ''
  expect(money, '收银台金额').toBe(amount)
  const ack = await epayNotify(world.provider.code, world.provider.merchant, world.provider.key, orderNo, money)
  expect(ack, '收银台回调回执').toBe('success')
  return `收银台金额 ${money}，回调回执 ${ack}`
}

// ----------------------------------------------------------------------------
//  页面上的几块
// ----------------------------------------------------------------------------

/** 门户顶部导航：我的套餐 / 选购 / 钱包 */
export async function nav(page: Page, name: '我的套餐' | '选购' | '钱包'): Promise<void> {
  await page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name, exact: true }).click()
}

/** 我的套餐里的一张卡（<article>）：按卡片标题（备注名或套餐名）找 */
export function card(page: Page, title: string): Locator {
  return page.locator('article').filter({ has: page.getByRole('heading', { name: title, exact: true }) })
}

/** 选购页、再买一份页里的一张套餐卡（<section>，标题是套餐名，里面有按钮链接） */
export function planCard(page: Page, name: string): Locator {
  return page
    .locator('section')
    .filter({ has: page.getByRole('heading', { name, exact: true }) })
    .filter({ has: page.getByRole('link') })
    .last()
}

/** 链接尾号「····abcd」（卡片、完成页的新链接）：取范围里第一个 */
export async function tailOf(scope: Locator | Page): Promise<string> {
  const t = await text(scope.getByText(/^····\S{4}$/).first())
  return t.replace('····', '')
}

/** 完成页「发生了什么」下面的几行 */
export async function happened(page: Page): Promise<string[]> {
  const list = page.getByRole('heading', { name: '发生了什么' }).locator('xpath=following-sibling::ul[1]')
  await expect(list).toBeVisible({ timeout: 20_000 })
  return (await list.getByRole('listitem').allInnerTexts()).map((s) => s.replace(/\s+/g, ' ').trim())
}

/**
 * 会建单、改账或写审计的那一下点击：排进 api.ts 的审计写锁（见那里的说明），点完等到这次写请求的响应回来再放锁。
 * 只认点击之后发出的第一个写请求（报价、预览、查单不算）
 */
export async function submit(page: Page, target: Locator): Promise<void> {
  await exclusive(async () => {
    const done = page.waitForResponse((r) => r.request().method() !== 'GET' && /\/v1\//.test(r.url()) && !/checkout\/quote|\/preview$|\/query$/.test(r.url()), { timeout: 20_000 })
    await target.click()
    await done
  })
}

/** 前提：在门户上买一份（确认页 → 付款页 → 收银台回调 → 完成页），停在我的套餐 */
export async function buyNew(page: Page, world: World, plan: { id: string; name: string }, yuan: string): Promise<void> {
  await go(page, `/checkout?new=${plan.id}`)
  await submit(page, page.getByRole('button', { name: `买${plan.name}，付 ¥${yuan}` }))
  await payByCashier(page, world, yuan)
  await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
  await backToSubs(page)
}

/**
 * 开关（ui/Switch）：透明的 checkbox 被轨道盖住，人点的是外面那层 label，这里也点 label。
 * 点完核对状态真的变了
 */
export async function setSwitch(page: Page, name: string, on: boolean): Promise<void> {
  const sw = page.getByRole('switch', { name })
  if ((await sw.isChecked()) !== on) await sw.locator('xpath=ancestor::label[1]').click()
  await expect(sw).toBeChecked({ checked: on })
}

/** 页面上第一段匹配的文字（压成一行） */
export async function seen(page: Page, pattern: RegExp | string): Promise<string> {
  const l = page.getByText(pattern).first()
  await expect(l).toBeVisible()
  return text(l)
}

/** 完成页：标题出来、回到我的套餐 */
export async function backToSubs(page: Page): Promise<void> {
  await page.getByRole('link', { name: '回到我的套餐' }).click()
  await expect(page.getByRole('heading', { level: 1, name: '我的套餐' })).toBeVisible()
}

/** 「11月8日」「2027年1月8日」→ [年|null, 月, 日] */
export function parseDay(s: string): [number | null, number, number] {
  const m = /(?:(\d{4})年)?(\d{1,2})月(\d{1,2})日/.exec(s)
  if (!m) throw new Error(`认不出日期：${s}`)
  return [m[1] ? Number(m[1]) : null, Number(m[2]), Number(m[3])]
}

/** b 是 a 往后 n 个月（月末溢出按服务端顺延规则只核月份） */
export function monthsLater(a: string, b: string, n: number): boolean {
  const [, ma] = parseDay(a)
  const [, mb] = parseDay(b)
  return ((ma - 1 + n) % 12) + 1 === mb || ((ma + n) % 12) + 1 === mb
}

/** 两个「M月D日」之间差几天（同一年或跨一年） */
export function daysBetween(a: string, b: string): number {
  const now = new Date()
  const [ya, ma, da] = parseDay(a)
  const [yb, mb, db] = parseDay(b)
  const ta = Date.UTC(ya ?? now.getFullYear(), ma - 1, da)
  let tb = Date.UTC(yb ?? now.getFullYear(), mb - 1, db)
  if (yb === null && tb < ta) tb = Date.UTC(now.getFullYear() + 1, mb - 1, db)
  return Math.round((tb - ta) / 86_400_000)
}
