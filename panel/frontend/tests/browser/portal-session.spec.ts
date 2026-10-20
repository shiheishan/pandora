import { newUser } from './api.ts'
import { expect, openPortal, step, test } from './fixtures.ts'

// ============================================================================
//  门户 S1：刷新后保持登录。访问令牌只在内存，不进任何存储；刷新页面后凭 HttpOnly 的
//  refresh cookie（pandora_rt，Path=/v1/auth）换回 access。localStorage 里只有不含秘密的登录标志
// ============================================================================

test('S1：刷新后保持登录', async ({ browser }) => {
  const page = await openPortal(browser, await newUser('s1'))

  await step(page, 'S1', async () => {
    const rt = (await page.context().cookies()).find((c) => c.name === 'pandora_rt')
    expect(rt, '登录后浏览器存下了 refresh cookie').toBeTruthy()
    expect(rt!.httpOnly, 'refresh cookie 是 HttpOnly').toBe(true)
    expect(rt!.path, 'refresh cookie 只随 /v1/auth 发出').toBe('/v1/auth')
    const storage = await page.evaluate(() => ({ keys: Object.keys(localStorage), hint: localStorage.getItem('pandora-portal-session') }))
    expect(storage.hint, '登录标志').toBe('1')
    expect(storage.keys.filter((k) => k.endsWith('-token')), 'localStorage 里没有令牌键').toEqual([])

    await page.reload()
    await expect(page.getByRole('navigation', { name: '主导航' })).toBeVisible()
    await expect(page.getByLabel('邮箱')).toHaveCount(0)
    return '登录后有 HttpOnly 的 pandora_rt（Path=/v1/auth），localStorage 只有登录标志、没有令牌；刷新后仍是登录态，看到主导航'
  })
})
