import { grant, newUser, sql, str, subscriptionsOf, uuid } from './api.ts'
import { card, expect, happened, narrowShot, openPortal, payByCashier, seen, step, test, text, submit } from './fixtures.ts'

// ============================================================================
//  过期卡片（w8buyfix 第 5 项）：「续费，恢复使用」与并列的次按钮「换个套餐」，再续费恢复。
//  文件名排在前面：要等 aegis-admin 的过期扫描（一分钟一轮）把状态翻成 expired，早开始早等完
// ============================================================================

test('过期卡片：续费恢复使用', async ({ browser, world }) => {
  test.setTimeout(240_000)
  const { std } = world.plans
  const u = await newUser('exp')
  await grant(u, std)
  const sub = str((await subscriptionsOf(u))[0], 'id')
  // SQL 夹具（时间流逝）：这一份的周期挪到过去，与 panel/tests/expiry_e2e.sh 同一句；状态由过期扫描自己翻
  sql('周期挪到过去', `UPDATE subscriptions SET current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 hour' WHERE id = '${uuid(sub)}';`)
  await expect
    .poll(async () => (await subscriptionsOf(u)).find((s) => s.id === sub)?.status, { message: '过期扫描把状态翻成 expired', timeout: 180_000, intervals: [5_000] })
    .toBe('expired')
  const page = await openPortal(browser, u)

  await step(page, 'EXP', async () => {
    const c = card(page, std.name)
    await expect(c).toContainText('已暂停使用')
    const renew = c.getByRole('link', { name: /^续费，恢复使用/ })
    const change = c.getByRole('link', { name: /^换个套餐/ })
    await expect(renew).toBeVisible()
    await expect(change).toBeVisible()
    await expect(change).toContainText('换成别的，马上恢复')
    const box = [await renew.boundingBox(), await change.boundingBox()]
    await narrowShot(page, 'EXP-card')
    const head = await text(c.getByRole('heading').locator('xpath=../..'))
    return `卡片「${head}」；主按钮「${await text(renew)}」，旁边次按钮「${await text(change)}」（1280 宽并排：${box.map((b) => Math.round(b?.width ?? 0)).join(' / ')}px）`
  })

  await step(page, 'EXPb', async () => {
    await card(page, std.name).getByRole('link', { name: /^续费，恢复使用/ }).click()
    await expect(page.getByRole('heading', { level: 1, name: '恢复使用' })).toBeVisible()
    const callout = await seen(page, /^付款后马上恢复使用。从今天起算/)
    await submit(page, page.getByRole('button', { name: '恢复使用，付 ¥30.00' }))
    const paid = await payByCashier(page, world, '30.00')
    await expect(page.getByRole('heading', { name: '已恢复使用' })).toBeVisible({ timeout: 20_000 })
    const lines = await happened(page)
    return `确认页「恢复使用」「${callout}」；${paid}；完成页「已恢复使用」「${lines[0]}」`
  })
})
