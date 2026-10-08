import { grant, newUser, subscriptionsOf, type User } from './api.ts'
import { card, expect, go, narrowShot, openAdmin, openPortal, payByCashier, seen, step, test, text, submit } from './fixtures.ts'
import type { Locator, Page } from '@playwright/test'

// ============================================================================
//  后台（w8walk C1–C4 与赠送）：人工开单弹窗。入口是用户抽屉「为其开单」跳的约定地址
//  #/billing/orders?new=<用户 id>（.claude/rules/screens-admin-billing.md），弹窗里预选这个用户
// ============================================================================

async function openManual(page: Page, u: User): Promise<Locator> {
  await page.goto(`/#/billing/orders?new=${u.id}`)
  const dlg = page.getByRole('dialog', { name: '人工开单' })
  await expect(dlg).toBeVisible()
  await expect(dlg.getByText(u.email)).toBeVisible()
  return dlg
}

/** 「套餐与周期」下拉：按「套餐名 · … ¥金额」选 */
async function pickPrice(dlg: Locator, plan: string, amount: string): Promise<string> {
  const select = dlg.getByLabel('套餐与周期')
  const option = select.locator('option').filter({ hasText: new RegExp(`^${plan} · .*¥${amount.replace('.', '\\.')}`) })
  const value = await option.first().getAttribute('value')
  if (!value) throw new Error(`下拉里没有 ${plan} ¥${amount}`)
  await select.selectOption(value)
  return text(option.first())
}

/** 落点区（多项时是 fieldset，只有一项时是带名字的 group） */
const placement = (dlg: Locator) => dlg.getByRole('group', { name: /^这单落到哪一份/ })

test('C1：后台开单的落点', async ({ browser, world }) => {
  const { std, pro } = world.plans
  const u = await newUser('c1')
  await grant(u, std)
  const page = await openAdmin(browser)
  let dlg: Locator

  await step(page, 'C1', async () => {
    dlg = await openManual(page, u)
    const price = await pickPrice(dlg, std.name, '30.00')
    const place = placement(dlg)
    await expect(place).toContainText(`续一期：${std.name}`)
    // 同款只续不新开：服务端只给「续一期」一项时不让选、直接写结果；多项时它必须是默认选中的那项
    const renew = place.getByRole('radio', { name: new RegExp(`^续一期：${std.name}`) })
    const many = (await renew.count()) > 0
    if (many) await expect(renew).toBeChecked()
    await expect(dlg.getByRole('button', { name: '创建订单' })).toBeEnabled()
    return `选「${price}」：落点「${await text(place)}」，${many ? '默认选中续这一份' : '只有这一项，不让选、直接写结果'}；按钮「创建订单」可点`
  })

  await step(page, 'C1b', async () => {
    const price = await pickPrice(dlg, pro.name, '42.00')
    const place = placement(dlg)
    await expect(place.getByRole('radio', { name: new RegExp(`把${std.name}换成${pro.name}`) })).toBeVisible()
    await expect(place.getByRole('radio', { checked: true })).toHaveCount(0)
    const submit = dlg.getByRole('button', { name: '先选落点' })
    await expect(submit).toBeDisabled()
    await narrowShot(page, 'C1b-dialog')
    return `换选「${price}」：落点「${await text(place)}」一个都不选，按钮灰着「先选落点」`
  })

  await step(page, 'C1c', async () => {
    await placement(dlg).getByRole('radio', { name: new RegExp(`把${std.name}换成${pro.name}`) }).check()
    await dlg.getByLabel('结算方式').selectOption({ label: '赠送（0 元）' })
    await dlg.getByLabel('开单原因（写入审计）').fill('w9browser 后台开单换套餐')
    await submit(page, dlg.getByRole('button', { name: '赠送开通' }))
    const toast = await seen(page, /已在原订阅上换套餐/)
    await expect(dlg).toBeHidden()
    const subs = await subscriptionsOf(u)
    expect(subs.map((s) => s.plan_id)).toEqual([pro.id])
    return `选「把${std.name}换成${pro.name}」后按钮「赠送开通」；提示「${toast}」；用户名下只有一份，已是 ${pro.name}`
  })
})

test('C2：待支付低于最低额，改用赠送', async ({ browser, world }) => {
  const { mini } = world.plans
  const u = await newUser('c2')
  const page = await openAdmin(browser)
  let dlg: Locator

  await step(page, 'C2', async () => {
    dlg = await openManual(page, u)
    await pickPrice(dlg, mini.name, '0.50')
    await expect(dlg.getByLabel('结算方式')).toHaveValue('pending')
    const warn = dlg.getByRole('alert').filter({ hasText: '低于支付渠道的最低付款额' })
    await expect(warn).toBeVisible()
    const said = await text(warn)
    expect(said).toContain('这单应付 ¥0.50，低于支付渠道的最低付款额 ¥1.00')
    await expect(warn.getByRole('button', { name: '改用赠送' })).toBeVisible()
    await expect(warn.getByRole('button', { name: '改用线下已收款' })).toBeVisible()
    await expect(dlg.getByRole('button', { name: '低于最低付款额' })).toBeDisabled()
    return `结算方式「待用户支付」时：「${said}」，按钮灰着「低于最低付款额」`
  })

  await step(page, 'C4b', async () => {
    await dlg.getByRole('button', { name: '改用赠送' }).click()
    await expect(dlg.getByLabel('结算方式')).toHaveValue('grant')
    await dlg.getByLabel('开单原因（写入审计）').fill('w9browser 后台赠送开通')
    await submit(page, dlg.getByRole('button', { name: '赠送开通' }))
    const toast = await seen(page, /已赠送开通，已另开一份订阅/)
    const portal = await openPortal(browser, u)
    await expect(card(portal, mini.name)).toBeVisible()
    return `点「改用赠送」后结算方式变成赠送，「赠送开通」；提示「${toast}」；门户我的套餐有${mini.name}`
  })
})

test('C3：后台待支付，用户在门户付', async ({ browser, world }) => {
  const { std } = world.plans
  const u = await newUser('c3')
  const page = await openAdmin(browser)

  await step(page, 'C3', async () => {
    const dlg = await openManual(page, u)
    await pickPrice(dlg, std.name, '30.00')
    await expect(placement(dlg)).toContainText(`另开一份${std.name}`)
    await dlg.getByLabel('开单原因（写入审计）').fill('w9browser 后台待支付开单')
    await submit(page, dlg.getByRole('button', { name: '创建订单' }))
    const toast = await seen(page, /已创建，等待用户在 30 分钟内支付/)

    const portal = await openPortal(browser, u)
    await go(portal, '/orders')
    const pending = portal.locator('section').filter({ hasText: '待支付' }).filter({ has: portal.getByRole('button', { name: '去支付' }) }).last()
    await expect(pending).toContainText('¥30.00')
    const row = await text(pending)
    await pending.getByRole('button', { name: '去支付' }).click()
    const modal = portal.getByRole('dialog')
    await submit(portal, modal.getByRole('button', { name: '支付宝' }))
    const paid = await payByCashier(modal, world, '30.00')
    await expect(modal.getByText(/已开通，有效期至/)).toBeVisible({ timeout: 20_000 })
    const ok = await text(modal.getByText(/已开通，有效期至/))
    await modal.getByRole('button', { name: '完成' }).click()
    await expect(card(portal, std.name)).toBeVisible()
    return `后台「${toast}」；门户订单页「${row}」→ 去支付 → 支付宝；${paid}；弹窗「${ok}」；我的套餐有${std.name}`
  })
})

test('C4：后台线下已收款', async ({ browser, world }) => {
  const { basic } = world.plans
  const u = await newUser('c4')
  const page = await openAdmin(browser)

  await step(page, 'C4', async () => {
    const dlg = await openManual(page, u)
    await pickPrice(dlg, basic.name, '10.00')
    await dlg.getByLabel('结算方式').selectOption({ label: '线下已收款' })
    await dlg.getByLabel('凭证号').fill(`W9B-OFFLINE-${u.id.slice(0, 6)}`)
    await dlg.getByLabel('开单原因（写入审计）').fill('w9browser 后台线下已收款')
    await submit(page, dlg.getByRole('button', { name: '入账并开通' }))
    const toast = await seen(page, /已按线下收款入账，已另开一份订阅/)
    const portal = await openPortal(browser, u)
    await expect(card(portal, basic.name)).toBeVisible()
    return `填凭证号后「入账并开通」；提示「${toast}」；门户我的套餐有${basic.name}`
  })
})
