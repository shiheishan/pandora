import { grant, newUser, sql, str, subscriptionsOf, uuid } from './api.ts'
import { card, expect, happened, narrowShot, openPortal, payByCashier, seen, step, test, text, submit } from './fixtures.ts'

// ============================================================================
//  过期卡片：「续费，恢复使用」与并列的次按钮「换个套餐」，再续费恢复。
//  文件名排在前面：要等 worker 的到期扫描 subscription:expire（一分钟一轮）把状态翻成 expired，早开始早等完
// ============================================================================

test('过期卡片：续费恢复使用', async ({ browser, world }) => {
  test.setTimeout(240_000)
  const { std } = world.plans
  const u = await newUser('exp')
  await grant(u, std)
  const sub = str((await subscriptionsOf(u))[0], 'id')
  // SQL 夹具（时间流逝）：这一份的周期与有周期的额度一起挪到过去，与 panel/tests/expiry_e2e.sh 同两句；状态由到期扫描自己翻
  sql(
    '周期挪到过去',
    `BEGIN;
UPDATE subscriptions SET current_period_start = now() - interval '31 days', current_period_end = now() - interval '1 hour' WHERE id = '${uuid(sub)}';
UPDATE quota_balances SET period_start = now() - interval '31 days', period_end = now() - interval '1 hour' WHERE subscription_id = '${uuid(sub)}' AND period_end IS NOT NULL;
COMMIT;`,
  )
  await expect
    .poll(async () => (await subscriptionsOf(u)).find((s) => s.id === sub)?.status, { message: '到期扫描把状态翻成 expired', timeout: 180_000, intervals: [5_000] })
    .toBe('expired')
  const page = await openPortal(browser, u)

  await step(page, 'EXP', async () => {
    const c = card(page, std.name)
    // 已到期的主卡：右上「已到期」，大字「X月X日到期了」，下面说节点停了、续费后链接不变
    await expect(c).toContainText('已到期')
    const gone = c.getByText(/到期了$/)
    await expect(gone).toBeVisible()
    await expect(c).toContainText('节点已经停了。续费后链接不变，App 里更新一次就能接着用。')
    const renew = c.getByRole('link', { name: /^续费，恢复使用/ })
    const change = c.getByRole('link', { name: /^换个套餐/ })
    const other = c.getByRole('link', { name: '另买一份', exact: true })
    await expect(renew).toBeVisible()
    await expect(change).toBeVisible()
    await expect(change).toContainText('换成别的，马上恢复')
    await expect(other).toBeVisible()
    const box = [await renew.boundingBox(), await change.boundingBox()]
    await narrowShot(page, 'EXP-card')
    return `卡片「${await text(gone)}」；主按钮「${await text(renew)}」，旁边「${await text(change)}」「另买一份」（1280 宽并排：${box.map((b) => Math.round(b?.width ?? 0)).join(' / ')}px）`
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
