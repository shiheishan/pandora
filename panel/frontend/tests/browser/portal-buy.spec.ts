import { expireOrderSql, newUser, setBalance, sql } from './api.ts'
import { backToSubs, card, expect, go, happened, monthsLater, nav, narrowShot, openPortal, payByCashier, planCard, setSwitch, step, tailOf, test, text, submit } from './fixtures.ts'

// ============================================================================
//  门户 A 组（w8walk A0–A5）：新购走渠道付、另买一份（不起名被拦、同款提示续费、起名）、
//  续费余额够、流量包挂到指定那份。一个用户从头走到尾，和真人一样一步接一步
// ============================================================================

test('A：新购、另买一份、续费、加流量', async ({ browser, world }) => {
  const { std } = world.plans
  const u = await newUser('a')
  const page = await openPortal(browser, u)

  await step(page, 'A1', async () => {
    await nav(page, '选购')
    await planCard(page, std.name).getByRole('link', { name: '买这个' }).click()
    await expect(page.getByRole('heading', { level: 1, name: `买${std.name}` })).toBeVisible()
    await expect(page.getByRole('radio', { name: /^1 个月/ })).toHaveAttribute('aria-checked', 'true')
    await expect(page.getByText('要付 ¥30.00', { exact: true })).toBeVisible()
    await narrowShot(page, 'A1-confirm')
    await submit(page, page.getByRole('button', { name: `买${std.name}，付 ¥30.00` }))
    await expect(page).toHaveURL(/#\/checkout\/pay\//)
    await expect(page.getByText('¥30.00', { exact: true })).toBeVisible()
    await narrowShot(page, 'A1-pay')
    const paid = await payByCashier(page, world, '30.00')
    await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
    const doneTail = await tailOf(page.getByRole('main'))
    const lines = await happened(page)
    await narrowShot(page, 'A1-done')
    await backToSubs(page)
    const subTail = await tailOf(card(page, std.name))
    expect(subTail, '完成页的新链接就是我的套餐里那一份').toBe(doneTail)
    return `确认页「要付 ¥30.00」、付款页 ¥30.00；${paid}；完成页「新的一份买好了」「${lines[0]}」新链接 ····${doneTail}；我的套餐那一份 ····${subTail}`
  })

  await step(page, 'A2', async () => {
    await page.getByRole('link', { name: /再买一份，分开用/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: '再买一份' })).toBeVisible()
    await planCard(page, std.name).getByRole('link', { name: '买这个' }).click()
    const name = page.getByLabel(/给它起个名字/)
    await expect(name).not.toHaveValue('')
    const prefilled = await name.inputValue()
    await name.fill('')
    const warn = page.getByText(/不起名的话，App 里会有两个「.+」，分不清哪个是哪个/)
    await expect(warn).toBeVisible()
    await expect(page.getByRole('button', { name: '先给它起个名字' })).toBeDisabled()
    return `名字预填「${prefilled}」；清空后「${await text(warn)}」，按钮灰着「先给它起个名字」`
  })

  await step(page, 'A2b', async () => {
    const tip = page.getByText(`你已经有一份${std.name}`)
    await expect(tip).toBeVisible()
    const seen = await text(tip)
    await page.getByRole('link', { name: '只想延长它？改成续费 →' }).click()
    await expect(page.getByRole('heading', { level: 1, name: '续费' })).toBeVisible()
    await page.goBack()
    await expect(page.getByRole('heading', { level: 1, name: '再买一份' })).toBeVisible()
    return `「${seen}」；点「只想延长它？改成续费 →」到了续费确认页`
  })

  await step(page, 'A3', async () => {
    await page.getByLabel(/给它起个名字/).fill('妈妈的')
    const shown = page.getByText(/App 里会显示成「.+ · 妈妈的」/)
    const shownText = await text(shown)
    await submit(page, page.getByRole('button', { name: '买一份新的，付 ¥30.00' }))
    await expect(page).toHaveURL(/#\/checkout\/pay\//)
    const paid = await payByCashier(page, world, '30.00')
    await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    expect(lines[0]).toContain(`妈妈的 · ${std.name}`)
    await backToSubs(page)
    await expect(page.locator('article')).toHaveCount(2)
    await expect(card(page, '妈妈的')).toBeVisible()
    await expect(card(page, std.name)).toBeVisible()
    return `「${shownText}」；${paid}；完成页「${lines[0]}」；我的套餐两张卡「${std.name}」「妈妈的」`
  })

  await step(page, 'A4', async () => {
    await setBalance(u, 5000, 'A4 续费余额够')
    await page.reload()
    await card(page, std.name).getByRole('link', { name: /^续费/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: '续费' })).toBeVisible()
    const callout = await text(page.getByText(/^到期日 .+ → .+。链接不变/))
    const [, from = '', to = ''] = /到期日 (.+?) → (.+?)。/.exec(callout) ?? []
    expect(monthsLater(from, to, 1), `到期日 ${from} → ${to} 是一个月`).toBe(true)
    await expect(page.getByText('余额够付，¥30.00 全部用余额')).toBeVisible()
    await narrowShot(page, 'A4-confirm')
    await submit(page, page.getByRole('button', { name: '续费，用余额付 ¥30.00' }))
    await expect(page.getByRole('heading', { name: '续费好了' })).toBeVisible({ timeout: 20_000 })
    // 余额读数在下单后才重新拉取：等页面写出扣过之后的余额再读（这一行中间还写了「在线付了」，是 B2c 的产品问题）
    await expect(page.getByText(/^余额付了 ¥30\.00，.*；余额还剩 ¥20\.00$/)).toBeVisible()
    const lines = await happened(page)
    expect(lines[0]).toContain(`用到 ${to}（原来 ${from}）`)
    return `确认页「${callout}」「余额够付，¥30.00 全部用余额」；完成页「${lines[0]}」「${lines[1]}」`
  })

  await step(page, 'A5', async () => {
    await backToSubs(page)
    await card(page, '妈妈的').getByRole('link', { name: /^加流量/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: '加流量' })).toBeVisible()
    await expect(page.getByText(`给 妈妈的 · ${std.name} 加流量`)).toBeVisible()
    await page.getByRole('radio', { name: /^12G/ }).click()
    await page.getByRole('link', { name: /^加 12G · ¥5\.00/ }).click()
    const callout = page.getByText(`12G 马上加到「妈妈的 · ${std.name}」，只给这份用，用完为止`)
    const calloutText = await text(callout)
    await setSwitch(page, '用余额', false)
    await expect(page.getByText('要付 ¥5.00', { exact: true })).toBeVisible()
    await submit(page, page.getByRole('button', { name: '加 12G，付 ¥5.00' }))
    const paid = await payByCashier(page, world, '5.00')
    await expect(page.getByRole('heading', { name: '已加 12G' })).toBeVisible({ timeout: 20_000 })
    await backToSubs(page)
    await expect(card(page, '妈妈的')).toContainText('含流量包 12G')
    await expect(card(page, std.name)).not.toContainText('含流量包')
    return `从「妈妈的」卡片加流量：「${calloutText}」，关掉余额后付 ¥5.00；${paid}；完成页「已加 12G」；只有「妈妈的」写含流量包 12G`
  })
})

// ----------------------------------------------------------------------------
//  A0：同款已有待付款单时再买。先下一张停在付款页不付，再从选购页买同一款
// ----------------------------------------------------------------------------

/** 下一张 W9 标准的新购单、停在付款页，返回订单 id */
async function leavePending(page: import('@playwright/test').Page, planId: string, button: string): Promise<string> {
  await go(page, `/checkout?new=${planId}`)
  await submit(page, page.getByRole('button', { name: button }))
  await expect(page).toHaveURL(/#\/checkout\/pay\//)
  const id = /#\/checkout\/pay\/([^?]+)/.exec(page.url())?.[1] ?? ''
  // 换一个页面实例再来：页面内存里记着刚下的单会直接重开它的付款，这里要看的是服务端拦下第二张
  await page.reload()
  return decodeURIComponent(id)
}

/** 报价通过时下单按钮会出来，点一下由建单拦下；报价就被拦时页面直接给提示，不点 */
async function clickBuyIfQuoted(page: import('@playwright/test').Page, button: string): Promise<void> {
  const buy = page.getByRole('button', { name: button })
  await expect(buy.or(page.getByText(/还没付款|超过付款期限/)).first()).toBeVisible()
  if (await buy.isVisible()) await submit(page, buy)
}

/** 确认页上关于那张待付款单的提示：取页面上实际出现的那句（拦在建单时是提示框，拦在报价时是加载失败） */
async function pendingNotice(page: import('@playwright/test').Page): Promise<string> {
  const notice = page.getByText(/还没付款|超过付款期限/).first()
  await expect(notice).toBeVisible()
  return text(notice.locator('xpath=..'))
}

// 产品问题（见 paths.ts 的 PRODUCT_ISSUES.A0）：报价接口先回 409 order_pending，确认页只显示「价格加载失败」
test.fixme('A0：同款已有待付款单', async ({ browser, world }) => {
  const { std } = world.plans
  const page = await openPortal(browser, await newUser('a0'))

  await step(page, 'A0', async () => {
    await leavePending(page, std.id, `买${std.name}，付 ¥30.00`)
    await nav(page, '选购')
    await planCard(page, std.name).getByRole('link', { name: '买这个' }).click()
    await clickBuyIfQuoted(page, `买${std.name}，付 ¥30.00`)
    const seen = await pendingNotice(page)
    await expect(page.getByRole('button', { name: '取消它' }), `页面上看到的：${seen}`).toBeVisible()
    await expect(page.getByRole('link', { name: '去付款' })).toBeVisible()
    await submit(page, page.getByRole('button', { name: '取消它' }))
    await expect(page.getByText('那张单已取消，没有扣钱。现在可以重新下单了')).toBeVisible()
    return `「${seen}」；给「取消它」「去付款」；取消后提示可以重新下单`
  })
})

test.fixme('A0b：那张待付款单已超过付款期限', async ({ browser, world }) => {
  const { std } = world.plans
  const page = await openPortal(browser, await newUser('a0b'))

  await step(page, 'A0b', async () => {
    const orderId = await leavePending(page, std.id, `买${std.name}，付 ¥30.00`)
    // SQL 夹具（时间流逝）：把这张单的付款期限拨到一分钟前，订单仍是待支付——与 w8walk C5 同一做法。
    // 只改 orders.expires_at，不动预留：释放任务按预留的到期找单，不会在测试中途把它关掉
    sql('付款期限拨到过去', expireOrderSql(orderId))
    await nav(page, '选购')
    await planCard(page, std.name).getByRole('link', { name: '买这个' }).click()
    await clickBuyIfQuoted(page, `买${std.name}，付 ¥30.00`)
    const seen = await pendingNotice(page)
    await expect(page.getByRole('button', { name: '取消它' }), `页面上看到的：${seen}`).toBeVisible()
    await expect(page.getByRole('link', { name: '去付款' })).toHaveCount(0)
    return `「${seen}」；只给「取消它」，没有「去付款」`
  })
})
