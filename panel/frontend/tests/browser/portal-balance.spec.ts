import { grant, newUser, setBalance } from './api.ts'
import { backToSubs, buyNew, card, expect, happened, narrowShot, nav, openPortal, payByCashier, planCard, seen, step, tailOf, test, text } from './fixtures.ts'
import type { Page } from '@playwright/test'

// ============================================================================
//  门户 B 组（w8walk B1–B6b）：余额与站点最低付款额（¥1.00）。每条路径一个新用户，
//  前提（赠送开一份、调余额）经后台接口造，要验证的那一步在页面上点
// ============================================================================

const yuan = (s: string) => Math.round(Number(s) * 100)
const fen = (n: number) => (n / 100).toFixed(2)

/** 卡片 → 换个套餐 → 选目标套餐那一行，停在确认页；返回那一行写的结果 */
async function changeTo(page: Page, from: string, to: string): Promise<string> {
  await card(page, from).getByRole('link', { name: '换个套餐' }).click()
  await expect(page.getByRole('heading', { level: 1, name: '换个套餐' })).toBeVisible()
  const link = page.getByRole('link', { name: `换成${to}`, exact: true })
  const row = await text(link.locator('xpath=..'))
  await link.click()
  await expect(page.getByRole('heading', { level: 1, name: `换成${to}` })).toBeVisible()
  return row
}

for (const [id, balance, applied, payable, kept] of [
  ['B1', 1200, '12.00', '18.00', '0.00'],
  ['B2', 2950, '29.00', '1.00', '0.50'],
] as const) {
  test(`${id}：续费，余额 ¥${fen(balance)}`, async ({ browser, world }) => {
    const { std } = world.plans
    const u = await newUser(id)
    await grant(u, std)
    await setBalance(u, balance, `${id} 续费余额不够`)
    const page = await openPortal(browser, u)

    await step(page, id, async () => {
      await card(page, std.name).getByRole('link', { name: /^续费/ }).click()
      await expect(page.getByRole('heading', { level: 1, name: '续费' })).toBeVisible()
      const sum = await seen(page, `余额抵 ¥${applied}，还需支付 ¥${payable}`)
      const note = id === 'B2' ? await seen(page, `最低要付 ¥1.00，所以这次余额只用 ¥${applied}，剩下 ¥${kept} 还在余额里`) : ''
      await narrowShot(page, `${id}-confirm`)
      await page.getByRole('button', { name: `续费，付 ¥${payable}` }).click()
      await expect(page.getByText(`¥${payable}`, { exact: true })).toBeVisible()
      const paid = await payByCashier(page, world, payable)
      await expect(page.getByRole('heading', { name: '续费好了' })).toBeVisible({ timeout: 20_000 })
      const lines = await happened(page)
      expect(lines[1]).toMatch(new RegExp(`余额付了 ¥${applied.replace('.', '\\.')}，.+付了 ¥${payable.replace('.', '\\.')}；余额还剩 ¥${kept.replace('.', '\\.')}`))
      return `确认页「${sum}」${note ? `「${note}」` : ''}；付款页 ¥${payable}；${paid}；完成页「${lines[1]}」`
    })
  })
}

test('B3：换贵的', async ({ browser, world }) => {
  const { std, pro } = world.plans
  const page = await openPortal(browser, await newUser('b3'))
  await buyNew(page, world, std, '30.00')
  const before = await tailOf(card(page, std.name))

  await step(page, 'B3', async () => {
    const row = await changeTo(page, std.name, pro.name)
    const formula = await seen(page, /^¥42\.00 − 原套餐没用完的 ¥[\d.]+ = 今天付 ¥[\d.]+$/)
    const [, credit = '', pay = ''] = /没用完的 ¥([\d.]+) = 今天付 ¥([\d.]+)/.exec(formula) ?? []
    expect(yuan(credit), '刚买的一份几乎没用：抵扣接近 ¥30').toBeGreaterThan(2900)
    expect(yuan(credit)).toBeLessThanOrEqual(3000)
    expect(4200 - yuan(credit), '算式自洽').toBe(yuan(pay))
    await narrowShot(page, 'B3-confirm')
    await page.getByRole('button', { name: `换成${pro.name}，付 ¥${pay}` }).click()
    const paid = await payByCashier(page, world, pay)
    await expect(page.getByRole('heading', { name: `已换成${pro.name}` })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    await backToSubs(page)
    const after = await tailOf(card(page, pro.name))
    expect(after, '换套餐链接不变').toBe(before)
    return `换个套餐页「${row}」；确认页「${formula}」；${paid}；完成页「${lines[0]}」；链接 ····${before} → ····${after}`
  })
})

test('B4：换便宜的', async ({ browser, world }) => {
  const { pro, basic } = world.plans
  const page = await openPortal(browser, await newUser('b4'))
  await buyNew(page, world, pro, '42.00')

  await step(page, 'B4', async () => {
    const row = await changeTo(page, pro.name, basic.name)
    const sum = await seen(page, /^这次不用付钱，多出的 ¥[\d.]+ 退到钱包余额$/)
    const refund = /¥([\d.]+)/.exec(sum)?.[1] ?? ''
    expect(yuan(refund), '进阶 ¥42 几乎没用，换成基础 ¥10 退回约 ¥32').toBeGreaterThan(3100)
    await page.getByRole('button', { name: `换成${basic.name}`, exact: true }).click()
    await expect(page.getByRole('heading', { name: `已换成${basic.name}` })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    expect(lines.join('；')).toContain(`¥${refund} 已退到余额，现在余额 ¥${refund}`)
    await nav(page, '钱包')
    await expect(page.getByText('账户余额')).toBeVisible()
    await expect(page.getByText(`¥${refund}`, { exact: true }).first()).toBeVisible()
    return `换个套餐页「${row}」；确认页「${sum}」；完成页「${lines[2]}」；钱包余额 ¥${refund}`
  })
})

test('B5：换套餐只差几分钱', async ({ browser, world }) => {
  const { std, plus } = world.plans
  const page = await openPortal(browser, await newUser('b5'))
  await buyNew(page, world, std, '30.00')

  await step(page, 'B5', async () => {
    await changeTo(page, std.name, plus.name)
    const sum = await seen(page, /^零头 ¥0\.\d\d 不到支付最低额，这次免了$/)
    const waived = /¥([\d.]+)/.exec(sum)?.[1] ?? ''
    await page.getByRole('button', { name: `换成${plus.name}`, exact: true }).click()
    await expect(page.getByRole('heading', { name: `已换成${plus.name}` })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    expect(lines.join('；')).toContain(`零头 ¥${waived} 已免`)
    return `确认页「${sum}」，按钮「换成${plus.name}」不用付款；完成页「${lines[2]}」`
  })
})

test('B6：低于最低额', async ({ browser, world }) => {
  const { mini } = world.plans
  const u = await newUser('b6')
  const page = await openPortal(browser, u)

  await step(page, 'B6', async () => {
    await nav(page, '选购')
    await planCard(page, mini.name).getByRole('link', { name: '买这个' }).click()
    await expect(page.getByRole('heading', { level: 1, name: `买${mini.name}` })).toBeVisible()
    const sum = await seen(page, /还差 ¥0\.50。在线付款最少要付 ¥1\.00，这一单付不了/)
    await expect(page.getByRole('button', { name: '余额不够，先充值再来' })).toBeDisabled()
    await expect(page.getByRole('link', { name: '去钱包充值（充 ¥1 就够），充完回来再点 →' })).toBeVisible()
    await narrowShot(page, 'B6-confirm')
    return `「${sum}」；按钮灰着「余额不够，先充值再来」；给「去钱包充值（充 ¥1 就够）」`
  })

  await step(page, 'B6b', async () => {
    await setBalance(u, 50, 'B6b 低于最低额但余额够')
    await page.reload()
    const line = await seen(page, '这单只要 ¥0.50，低于支付最低额，只能用余额付')
    const sw = page.getByRole('switch', { name: '用余额' })
    await expect(sw).toBeChecked()
    await expect(sw).toBeDisabled()
    await page.getByRole('button', { name: `买${mini.name}，用余额付 ¥0.50` }).click()
    await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    expect(lines[1]).toContain('余额付了 ¥0.50')
    return `「${line}」开关锁住；按钮「买${mini.name}，用余额付 ¥0.50」；完成页「${lines[1]}」`
  })
})
