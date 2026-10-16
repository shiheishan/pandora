import { grant, newUser } from './api.ts'
import { backToSubs, card, expect, happened, narrowShot, nav, openPortal, payByCashier, planCard, seen, step, tailOf, test, text, submit } from './fixtures.ts'
import type { Page } from '@playwright/test'

// ============================================================================
//  门户 D 组：一次性套餐（按流量，10-16）。一个用户从头走到尾：选购页「一次性」标签里新购，
//  再从选购页买同一款时先选落点、叠加到那一份，最后把它换成按时间的套餐（跨类型换掉，抵扣按剩余比例）
// ============================================================================

const yuan = (s: string) => Math.round(Number(s) * 100)

/** 选购页的「按时间 / 一次性」标签 */
const kindTab = (page: Page, name: '按时间' | '一次性') => page.getByRole('tablist', { name: '套餐类型' }).getByRole('tab', { name: new RegExp(`^${name}`) })

test('D：一次性新购、叠加、换成按时间的套餐', async ({ browser, world }) => {
  const { once, std } = world.plans
  const page = await openPortal(browser, await newUser('d'))
  let tail = ''

  await step(page, 'D1', async () => {
    await nav(page, '选购')
    await kindTab(page, '一次性').click()
    await expect(kindTab(page, '一次性')).toHaveAttribute('aria-selected', 'true')
    await planCard(page, once.name).getByRole('link', { name: '买这个' }).click()
    await expect(page.getByRole('heading', { level: 1, name: `买${once.name}` })).toBeVisible()
    const validity = await seen(page, /^今天起 30 天，到 /)
    await expect(page.getByText('要付 ¥5.00', { exact: true })).toBeVisible()
    await narrowShot(page, 'D1-confirm')
    await submit(page, page.getByRole('button', { name: `买${once.name}，付 ¥5.00` }))
    await expect(page).toHaveURL(/#\/checkout\/pay\//)
    const paid = await payByCashier(page, world, '5.00')
    await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    await backToSubs(page)
    const c = card(page, once.name)
    await expect(c).toContainText('剩 20G / 共 20G')
    tail = await tailOf(c)
    return `确认页有效期「${validity}」「要付 ¥5.00」；${paid}；完成页「${lines[0]}」；卡片「剩 20G / 共 20G」····${tail}`
  })

  await step(page, 'D2', async () => {
    await nav(page, '选购')
    // 名下只有一次性套餐：选购页默认停在「一次性」；同款那张卡的按钮是「再买一份」
    await expect(kindTab(page, '一次性')).toHaveAttribute('aria-selected', 'true')
    await planCard(page, once.name).getByRole('link', { name: '再买一份' }).click()
    await expect(page.getByRole('heading', { level: 1, name: `买${once.name}` })).toBeVisible()
    const choices = page.getByRole('radiogroup', { name: `${once.name}放到哪里` })
    await expect(choices.getByRole('radio', { checked: true })).toHaveCount(0)
    const options = (await choices.getByRole('radio').allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim())
    expect(options.some((o) => o.startsWith('另开一份')), `选项里要有另开一份：${options.join(' / ')}`).toBe(true)
    await narrowShot(page, 'D2-landing')
    await choices.getByRole('radio', { name: new RegExp(`^叠加到你的${once.name}`) }).click()
    await expect(page.getByRole('heading', { level: 1, name: '叠加' })).toBeVisible()
    const traffic = await seen(page, '剩 20G → 加完 40G')
    await submit(page, page.getByRole('button', { name: '叠加，付 ¥5.00' }))
    const paid = await payByCashier(page, world, '5.00')
    await expect(page.getByRole('heading', { name: '叠加好了' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    expect(lines[0]).toContain('总流量加上 20G')
    await backToSubs(page)
    const c = card(page, once.name)
    await expect(c).toContainText('剩 40G / 共 40G')
    expect(await tailOf(c), '叠加链接不变').toBe(tail)
    return `选项 ${options.map((o) => `「${o}」`).join('')} 不预选；选叠加后「${traffic}」；${paid}；完成页「${lines[0]}」；卡片「剩 40G / 共 40G」，链接 ····${tail} 不变`
  })

  await step(page, 'D3', async () => {
    await card(page, once.name).getByRole('link', { name: '换个套餐' }).click()
    await expect(page.getByRole('heading', { level: 1, name: '换个套餐' })).toBeVisible()
    const link = page.getByRole('link', { name: `换成${std.name}`, exact: true })
    const row = await text(link.locator('xpath=..'))
    await link.click()
    await expect(page.getByRole('heading', { level: 1, name: `换成${std.name}` })).toBeVisible()
    const formula = await seen(page, /^¥[\d.]+ − 原套餐没用完的 ¥[\d.]+ = 今天付 ¥[\d.]+$/)
    const [, price = '', credit = '', pay = ''] = /^¥([\d.]+) − 原套餐没用完的 ¥([\d.]+) = 今天付 ¥([\d.]+)$/.exec(formula) ?? []
    // 新购与叠加各付 ¥5.00、几乎没用：按剩余流量与天数里少的那个比例抵扣，不超过付过的 ¥10.00
    expect(yuan(credit), '一次性没用完的部分算进价钱').toBeGreaterThan(400)
    expect(yuan(credit)).toBeLessThanOrEqual(1000)
    expect(yuan(price) - yuan(credit), '算式自洽').toBe(yuan(pay))
    await submit(page, page.getByRole('button', { name: `换成${std.name}，付 ¥${pay}` }))
    const paid = await payByCashier(page, world, pay)
    await expect(page.getByRole('heading', { name: `已换成${std.name}` })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    await backToSubs(page)
    expect(await tailOf(card(page, std.name)), '换套餐链接不变').toBe(tail)
    return `换个套餐页「${row}」；确认页「${formula}」；${paid}；完成页「${lines[0]}」；链接 ····${tail} 不变`
  })
})

// 产品问题（paths.ts 的 PRODUCT_ISSUES.D4）：名下只有一次性套餐时点「按时间」标签回不到按时间的套餐
test.fixme('D4：只有一次性套餐时切到「按时间」', async ({ browser, world }) => {
  const { once, std } = world.plans
  const u = await newUser('d4')
  await grant(u, once)
  const page = await openPortal(browser, u)
  await step(page, 'D4', async () => {
    await nav(page, '选购')
    await expect(kindTab(page, '一次性')).toHaveAttribute('aria-selected', 'true')
    await kindTab(page, '按时间').click()
    await expect(kindTab(page, '按时间')).toHaveAttribute('aria-selected', 'true')
    await expect(planCard(page, std.name)).toBeVisible()
    return `默认「一次性」；点「按时间」后列出${std.name}`
  })
})
