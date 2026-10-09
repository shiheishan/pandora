import { admin, grant, newUser, subscriptionsOf, sql, str, uuid, type User } from './api.ts'
import { backToSubs, card, daysBetween, expect, happened, monthsLater, narrowShot, nav, openPortal, step, test, text, submit } from './fixtures.ts'
import { giftCode } from './seed.ts'
import type { Page } from '@playwright/test'

// ============================================================================
//  门户 C6（礼品卡七种用法）与 C7（流量包跟着那一份，在用的不能挪）
// ============================================================================

/** 从我的套餐底部「有卡？去兑换」进兑换页，输卡号、查询，停在卡面 */
async function lookup(page: Page, code: string): Promise<string> {
  await nav(page, '我的套餐')
  await page.getByRole('link', { name: '有卡？去兑换' }).click()
  await expect(page.getByRole('heading', { level: 1, name: '兑换卡' })).toBeVisible()
  await page.getByLabel('卡号').fill(code)
  await page.getByRole('button', { name: '查询' }).click()
  const face = page.getByText('这张卡', { exact: true }).locator('xpath=following-sibling::h3[1]')
  await expect(face).toBeVisible()
  return text(face)
}

/** 卡面下「会发生什么」那一句 */
const sentence = (page: Page) => text(page.getByText('会发生什么', { exact: true }).locator('xpath=..'))

/** 前提：后台「线下已收款」开一份——付了钱的一份，换套餐时才有没用完的钱可退（与 w8walk 先渠道付同效） */
async function paidCopy(u: User, plan: { id: string; prices: string[] }): Promise<void> {
  await admin('/v1/orders/manual', {
    body: { user_id: u.id, plan_id: plan.id, price_id: plan.prices[0], reason: 'w9browser 造前提：线下已收款开一份', settlement: 'offline', reference: `W9B-${u.id.slice(0, 8)}`, target: { kind: 'new' } },
    expect: [200, 201],
  })
}

test('C6：礼品卡', async ({ browser, world }) => {
  const { std, pro } = world.plans
  const u = await newUser('c6')
  await paidCopy(u, std)
  const page = await openPortal(browser, u)

  await step(page, 'C6a', async () => {
    const face = await lookup(page, await giftCode(world.cards.days7))
    const s = await sentence(page)
    const [, from = '', to = ''] = /到期日 (.+?) → (.+?)。/.exec(s) ?? []
    expect(daysBetween(from, to), `${from} → ${to} 是 7 天`).toBe(7)
    await narrowShot(page, 'C6a-card')
    await submit(page, page.getByRole('button', { name: '兑换，加 7 天' }))
    await expect(page.getByRole('heading', { name: '兑换好了' })).toBeVisible()
    const lines = await happened(page)
    expect(lines[0]).toContain(`用到 ${to}（原来 ${from}）`)
    return `卡面「${face}」；「${s}」；完成页「${lines[0]}」`
  })

  await step(page, 'C6b', async () => {
    // SQL 夹具：这一份本期已用设成 5G（模拟用过；按节点上报造要先查出节点侧的用户编号，绕一大圈），与 w8walk C6b 同一做法
    const sub = str((await subscriptionsOf(u))[0], 'id')
    sql('本期已用 5G', `UPDATE quota_balances SET consumed = 5368709120 WHERE subscription_id = '${uuid(sub)}' AND metric = 'traffic.bytes';`)
    const face = await lookup(page, await giftCode(world.cards.reset))
    const s = await sentence(page)
    expect(s).toContain('已用 5G → 0')
    await submit(page, page.getByRole('button', { name: '兑换，流量清零重算' }))
    await expect(page.getByRole('heading', { name: '流量已清零重算' })).toBeVisible()
    const lines = await happened(page)
    await backToSubs(page)
    await expect(card(page, std.name)).toContainText('剩 100G / 共 100G')
    return `卡面「${face}」；「${s}」；完成页「${lines[0]}」；卡片「剩 100G / 共 100G」`
  })

  await step(page, 'C6c', async () => {
    const face = await lookup(page, await giftCode(world.cards.traffic2g))
    const s = await sentence(page)
    await submit(page, page.getByRole('button', { name: '兑换，加 2G' }))
    await expect(page.getByRole('heading', { name: '已加 2G' })).toBeVisible()
    const lines = await happened(page)
    await backToSubs(page)
    await expect(card(page, std.name)).toContainText('含流量包 2G')
    return `卡面「${face}」；「${s}」；完成页「${lines[0]}」；卡片写含流量包 2G`
  })

  await step(page, 'C6d', async () => {
    const face = await lookup(page, await giftCode(world.cards.planStd))
    const renew = page.getByRole('radio', { name: new RegExp(`续到你的${std.name}`) })
    if ((await renew.count()) > 0) await expect(renew).toHaveAttribute('aria-checked', 'true')
    const s = await sentence(page)
    const [, from = '', to = ''] = /到期日 (.+?) → (.+?)。/.exec(s) ?? []
    expect(monthsLater(from, to, 1), `${from} → ${to} 是一个月`).toBe(true)
    await submit(page, page.getByRole('button', { name: `兑换，续到 ${to}` }))
    await expect(page.getByRole('heading', { name: '兑换好了' })).toBeVisible()
    const lines = await happened(page)
    return `卡面「${face}」；默认「续到你的${std.name}」；「${s}」；完成页「${lines[0]}」`
  })

  await step(page, 'C6e', async () => {
    const face = await lookup(page, await giftCode(world.cards.planPro))
    const choices = page.getByRole('radiogroup', { name: '怎么用这张卡' })
    await expect(choices).toBeVisible()
    await expect(choices.getByRole('radio', { checked: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: '先选一种用法' })).toBeDisabled()
    const options = (await choices.getByRole('radio').allInnerTexts()).map((t) => t.replace(/\s+/g, ' ').trim())
    await narrowShot(page, 'C6e-choose')
    await choices.getByRole('radio', { name: new RegExp(`(升级成|换成)${pro.name}`) }).click()
    const s = await sentence(page)
    expect(s).toMatch(new RegExp(`今天换成${pro.name}，从今天起算，到 .+`))
    // 只退已付价值、赠送的时长不保留（用户 10-08）：C6a 加的 7 天 + C6d 套餐卡续的一个月（28–31 天），选之前写明
    const gift = /；赠送的 (\d+) 天不保留。$/.exec(s)
    expect(gift, `选换掉后的说明要写赠送的天数不保留：${s}`).not.toBeNull()
    expect(Number(gift![1])).toBeGreaterThanOrEqual(35)
    expect(Number(gift![1])).toBeLessThanOrEqual(38)
    await submit(page, page.getByRole('button', { name: `兑换，换成${pro.name}` }))
    await expect(page.getByRole('heading', { name: `已换成${pro.name}` })).toBeVisible()
    const lines = await happened(page)
    return `卡面「${face}」；不预选、按钮灰着，选项：${options.map((o) => `「${o}」`).join('')}；选换掉后「${s}」；完成页「${lines.join('；')}」`
  })

  // 没有订阅的人兑盲盒：另一个用户、另一个浏览器上下文
  const nobody = await newUser('c6f')
  const other = await openPortal(browser, nobody)
  const mystery = await giftCode(world.cards.mystery)
  await step(other, 'C6f', async () => {
    await expect(other.getByText('你还没有套餐')).toBeVisible()
    await other.getByRole('link', { name: '有卡？去兑换' }).click()
    await other.getByLabel('卡号').fill(mystery)
    await other.getByRole('button', { name: '查询' }).click()
    const go = other.getByRole('button', { name: '兑换，抽一次' })
    const err = other.getByRole('alert').first()
    await expect(go.or(err).first()).toBeVisible()
    if (await go.isVisible()) await submit(other, go)
    await expect(err).toBeVisible()
    const why = await text(err)
    await expect(other.getByText('兑换过的卡会显示在这里。')).toBeVisible()
    return `被拦：「${why}」；兑换过的卡仍为空`
  })

  await step(page, 'C6g', async () => {
    const face = await lookup(page, mystery)
    const s = await sentence(page)
    await submit(page, page.getByRole('button', { name: '兑换，抽一次' }))
    const lines = await happened(page)
    expect(lines.join('；')).toMatch(/抽中了「(加 3 天|送 1G)」/)
    return `卡面「${face}」；「${s}」；完成页「${lines.join('；')}」`
  })
})

test('C7：流量包跟着那一份，在用的不能挪', async ({ browser, world }) => {
  const { basic, std } = world.plans
  const u = await newUser('c7')
  await grant(u, basic)
  await grant(u, std, { kind: 'new' })
  const subs = await subscriptionsOf(u)
  const stdSub = str(subs.find((s) => s.plan_id === std.id), 'id')
  // 后台按订阅给标准版加 1G：挂在标准版上，两份都在用
  await admin(`/v1/subscriptions/${stdSub}/traffic-pack`, { body: { bytes: 1024 ** 3, reason: 'w9browser C7 流量包挂在标准版' }, expect: [200, 201] })
  const page = await openPortal(browser, u)

  await step(page, 'C7', async () => {
    const from = card(page, std.name)
    const usage = from.getByText(/含流量包 1G/)
    await expect(usage).toBeVisible()
    await expect(card(page, basic.name)).not.toContainText('含流量包')
    // 用户 10-09 删掉了「升级前的旧流量包挪一次」：流量包只在那份彻底停用后才能转走，两份都在用时页面上没有挪的入口
    await expect(page.getByText(/挪|升级前买的/)).toHaveCount(0)
    await expect(page.getByRole('button', { name: /^挪到/ })).toHaveCount(0)
    await narrowShot(page, 'C7-cards')
    return `标准版写「${await text(usage)}」，基础版没有流量包；页面上没有挪的按钮和提示`
  })
})
