import { expireOrderSql, grant, newUser, sql } from './api.ts'
import { backToSubs, card, expect, happened, nav, narrowShot, openPortal, payByCashier, planCard, seen, step, tailOf, test, text, submit } from './fixtures.ts'

// ============================================================================
//  门户 C5（超时单）与 C8（改名、按份换新链接、完成页的 App 更新说明）
// ============================================================================

test('C5：超时单', async ({ browser, world }) => {
  const { basic } = world.plans
  const page = await openPortal(browser, await newUser('c5'))

  await step(page, 'C5', async () => {
    await nav(page, '选购')
    await planCard(page, basic.name).getByRole('link', { name: '买这个' }).click()
    await submit(page, page.getByRole('button', { name: `买${basic.name}，付 ¥10.00` }))
    await expect(page).toHaveURL(/#\/checkout\/pay\//)
    await expect(page.getByRole('link', { name: /在这台电脑上付款|付款页/ })).toBeVisible()
    const orderId = decodeURIComponent(/#\/checkout\/pay\/([^?]+)/.exec(page.url())?.[1] ?? '')
    // SQL 夹具（时间流逝）：付款期限拨到一分钟前，订单仍是待支付（与 w8walk C5 同一做法）。
    // 只改 orders.expires_at、不动预留：释放任务按预留的到期找单，不会在测试中途把它关掉
    sql('付款期限拨到过去', expireOrderSql(orderId))
    await page.reload()
    const title = await seen(page, '这张单已超过付款期限')
    const how = await seen(page, '取消它（不会扣钱），再重新下单就行。')
    await expect(page.getByRole('button', { name: '取消这张单，重新下单' })).toBeVisible()
    await expect(page.getByRole('link', { name: /在这台电脑上付款|付款页/ })).toHaveCount(0)
    await narrowShot(page, 'C5-lapsed')
    return `付款页「${title}」「${how}」，只给「取消这张单，重新下单」`
  })

  await step(page, 'C5b', async () => {
    await submit(page, page.getByRole('button', { name: '取消这张单，重新下单' }))
    await expect(page).toHaveURL(/#\/plans/)
    await planCard(page, basic.name).getByRole('link', { name: '买这个' }).click()
    await submit(page, page.getByRole('button', { name: `买${basic.name}，付 ¥10.00` }))
    const paid = await payByCashier(page, world, '10.00')
    await expect(page.getByRole('heading', { name: '新的一份买好了' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    return `取消后回到选购页；重下的单 ${paid}；完成页「${lines[0]}」`
  })
})

test('C8：改名与按份换新链接', async ({ browser, world }) => {
  const { std, basic } = world.plans
  const u = await newUser('c8')
  await grant(u, std)
  await grant(u, basic)
  const page = await openPortal(browser, u)
  const naming = page.getByRole('dialog', { name: '给这份起个名字' })
  const rotate = page.getByRole('dialog', { name: '换一个新链接？' })

  await step(page, 'C8', async () => {
    await card(page, basic.name).getByRole('button', { name: '起个名字', exact: true }).click()
    await naming.getByLabel('名字', { exact: true }).fill('妈妈的')
    await submit(page, naming.getByRole('button', { name: '保存' }))
    const toast = await seen(page, /已改名，App 里更新一次后显示「.+ · 妈妈的」/)
    await expect(card(page, '妈妈的')).toBeVisible()
    await card(page, std.name).getByRole('button', { name: '起个名字', exact: true }).click()
    await naming.getByLabel('名字', { exact: true }).fill('妈妈的')
    await submit(page, naming.getByRole('button', { name: '保存' }))
    const err = naming.getByText(`这个名字已经用在「妈妈的 · ${basic.name}」上了，换一个吧`)
    await expect(err).toBeVisible()
    const why = await text(err)
    await narrowShot(page, 'C8-taken')
    await naming.getByLabel('名字', { exact: true }).fill('我的手机')
    await submit(page, naming.getByRole('button', { name: '保存' }))
    await expect(card(page, '我的手机')).toBeVisible()
    return `给基础版起名「妈妈的」：「${toast}」；标准版也叫「妈妈的」时输入框下「${why}」；改叫「我的手机」成功`
  })

  await step(page, 'C8b', async () => {
    const before = await tailOf(card(page, '我的手机'))
    await card(page, '我的手机').getByRole('button', { name: '换新链接', exact: true }).click()
    const warn = await text(rotate.getByText('会发生什么', { exact: true }).locator('xpath=..'))
    await submit(page, rotate.getByRole('button', { name: '换新链接' }))
    await expect(page.getByRole('heading', { name: '已换新链接' })).toBeVisible()
    // 链接列表换好后重新拉取：等新链接的尾号出来再读
    await expect(page.getByRole('main').getByText(/^····\S{4}$/).first(), '换了新链接，尾号要变').not.toHaveText(`····${before}`)
    const after = await tailOf(page.getByRole('main'))
    const lines = await happened(page)
    await narrowShot(page, 'C8b-rotated')
    await backToSubs(page)
    await card(page, '我的手机').getByRole('button', { name: '换新链接' }).click()
    await submit(page, rotate.getByRole('button', { name: '换新链接' }))
    const wait = rotate.getByRole('button', { name: /^刚换过，\d+ 分钟后才能再换$/ })
    await expect(wait).toBeDisabled()
    const waitText = await text(wait)
    await rotate.getByRole('button', { name: '不换了' }).click()
    const momBefore = await tailOf(card(page, '妈妈的'))
    await card(page, '妈妈的').getByRole('button', { name: '换新链接' }).click()
    await submit(page, rotate.getByRole('button', { name: '换新链接' }))
    await expect(page.getByRole('heading', { name: '已换新链接' })).toBeVisible()
    await expect(page.getByRole('main').getByText(/^····\S{4}$/).first()).not.toHaveText(`····${momBefore}`)
    const momAfter = await tailOf(page.getByRole('main'))
    return `弹层「${warn}」；换好后「${lines[0]}」，····${before} → ····${after}；马上再换按钮灰着「${waitText}」；另一份 ····${momBefore} → ····${momAfter}`
  })

  await step(page, 'C8c', async () => {
    await page.getByRole('link', { name: '不知道点哪？看看你的 App 在哪点更新 ›' }).click()
    await expect(page).toHaveURL(/#\/help\?update=/)
    await expect(page.getByRole('heading', { name: '在 App 里点一次「更新」' })).toBeVisible()
    const device = await text(page.getByRole('radiogroup', { name: '你的设备' }).getByRole('radio', { checked: true }))
    const apps = (await page.getByRole('article').getByRole('heading', { level: 3 }).allInnerTexts()).map((s) => s.trim())
    expect(apps.length, '这台设备至少有一个 App 的说明').toBeGreaterThan(0)
    // 引用 App 自己的按钮名（用户 10-08 定的例外）：照它的中文界面写、按钮样式，第一次出现补半句说明
    await page.getByRole('radiogroup', { name: '你的设备' }).getByRole('radio', { name: '安卓手机' }).click()
    const v2rayNG = page.getByRole('article').locator('section').filter({ has: page.getByRole('heading', { name: 'v2rayNG', exact: true }) })
    await expect(v2rayNG.getByText('更新订阅', { exact: true })).toBeVisible()
    const tap = await text(v2rayNG.getByRole('listitem').nth(1))
    expect(tap).toContain('更新订阅（就是更新你添加的那条链接）')
    return `换新链接完成页的链接打开帮助中心「在 App 里点一次「更新」」，设备选中「${device}」，有 ${apps.join('、')} 的三步说明；Android 下 v2rayNG 写「${tap}」`
  })
})
