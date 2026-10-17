import type { Server } from 'node:http'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { MOCK_ACCOUNTS } from '../dev/mock-api'
import { bearer, close, loginAs, mockFetch, serve } from './mock-helpers'

describe('mock api · admin users edit', () => {
  let server: Server
  let base: string
  let auth: Record<string, string>
  let adminUserId: string

  beforeAll(async () => {
    ;({ server, base } = await serve('admin'))
    const login = await loginAs(base, MOCK_ACCOUNTS.admin)
    adminUserId = login.user_id
    auth = bearer(login.access_token)
  })
  afterAll(() => close(server))

  const get = (path: string) => mockFetch(base, auth, 'GET', path)
  const send = (method: string, path: string, body: unknown) => mockFetch(base, auth, method, path, body)

  const reauth = async () => {
    const res = await fetch(`${base}/v1/auth/reauth`, { method: 'POST', headers: auth, body: JSON.stringify({ password: MOCK_ACCOUNTS.admin.password }) })
    auth = { Authorization: `Bearer ${((await res.json()) as { access_token: string }).access_token}` }
  }

  type UserRow = { id: string; email: string; status: string }
  type UserDetail = {
    id: string
    email: string
    email_verified: boolean
    status: string
    display_name: string | null
    admin_note: string | null
    deleted_at: string | null
    referrer: { id: string; email: string } | null
    is_admin: boolean
    subscriptions: Array<{ status: string }>
  }

  async function detail(id: string): Promise<UserDetail> {
    return (await (await get(`/v1/users/${id}`)).json()) as UserDetail
  }

  /** 按条件挑一个种子用户；跳过管理员（种子里最新注册的那位是管理员，不能注销） */
  async function pickUser(predicate: (u: UserRow) => boolean): Promise<UserRow> {
    const list = (await (await get('/v1/users?limit=100')).json()) as { users: UserRow[] }
    for (const u of list.users) {
      if (predicate(u) && !(await detail(u.id)).is_admin) return u
    }
    throw new Error('no matching seed user')
  }

  it('changes email after reauth and marks it unverified', async () => {
    const u = await pickUser((x) => x.status === 'active' && x.id !== adminUserId && !x.email.startsWith('edit-mail-'))
    const newEmail = `edit-mail-${u.id.slice(0, 8)}@example.test`
    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    expect((await send('POST', `/v1/users/${u.id}/email`, { email: newEmail, reason: '用户申请换绑邮箱' })).status).toBe(403)
    await reauth()
    expect((await send('POST', `/v1/users/${u.id}/email`, { email: newEmail, reason: '用户申请换绑邮箱' })).status).toBe(200)
    const d = await detail(u.id)
    expect(d.email).toBe(newEmail)
    expect(d.email_verified).toBe(false)
  })

  it('refuses email change when the address is taken', async () => {
    const taken = await pickUser((x) => x.status === 'active' && x.id !== adminUserId)
    const other = await pickUser((x) => x.status === 'active' && x.id !== taken.id && x.id !== adminUserId)
    const res = await send('POST', `/v1/users/${other.id}/email`, { email: taken.email, reason: '测试邮箱撞号场景' })
    expect(res.status).toBe(409)
    expect(await res.json()).toMatchObject({ error: { message: '这个邮箱已被别的账号使用' } })
  })

  it('updates admin note without touching display name', async () => {
    const u = await pickUser((x) => x.status === 'active' && x.id !== adminUserId)
    const before = await detail(u.id)
    expect((await send('POST', `/v1/users/${u.id}/note`, { admin_note: '退款跟进中' })).status).toBe(200)
    const after = await detail(u.id)
    expect(after.admin_note).toBe('退款跟进中')
    expect(after.display_name).toBe(before.display_name)
  })

  it('validates referrer: self, downstream, and clear', async () => {
    const root = await pickUser((x) => x.status === 'active' && x.id !== adminUserId)
    const child = await pickUser((x) => x.status === 'active' && x.id !== root.id && x.id !== adminUserId)
    await send('POST', `/v1/users/${child.id}/referrer`, { referrer_id: root.id, reason: '种子邀请关系' })

    const selfTry = await send('POST', `/v1/users/${root.id}/referrer`, { referrer_id: root.id, reason: '测试不能设成自己' })
    expect(selfTry.status).toBe(422)
    expect(await selfTry.json()).toMatchObject({ error: { fields: { referrer_id: '不能把邀请人设成自己' } } })

    const cycleTry = await send('POST', `/v1/users/${root.id}/referrer`, { referrer_id: child.id, reason: '测试不能设成下线' })
    expect(cycleTry.status).toBe(422)
    expect(await cycleTry.json()).toMatchObject({ error: { fields: { referrer_id: '不能设成他自己邀请来的人（含下级的下级）' } } })

    expect((await send('POST', `/v1/users/${child.id}/referrer`, { referrer_id: null, reason: '清除邀请人测试' })).status).toBe(200)
    expect((await detail(child.id)).referrer).toBeNull()
  })

  it('deletes a normal user and blocks further edits; refuses admin', async () => {
    const victim = await pickUser((x) => x.status === 'active' && !x.email.startsWith('del-victim-') && x.id !== adminUserId)
    const activeSubs = (await detail(victim.id)).subscriptions.filter((s) => s.status === 'active').length

    // 登录的管理员本人不在种子用户里：按 id 命中「不能注销自己」
    const selfRefuse = await send('POST', `/v1/users/${adminUserId}/delete`, { reason: '尝试注销自己的账号' })
    expect(selfRefuse.status).toBe(409)
    expect(await selfRefuse.json()).toMatchObject({ error: { message: '不能注销自己的账号' } })

    // 种子里第一位是管理员
    const list = (await (await get('/v1/users?limit=100')).json()) as { users: UserRow[] }
    let staffId: string | null = null
    for (const row of list.users) {
      if ((await detail(row.id)).is_admin) {
        staffId = row.id
        break
      }
    }
    expect(staffId).not.toBeNull()
    const adminRefuse = await send('POST', `/v1/users/${staffId}/delete`, { reason: '尝试注销管理员账号' })
    expect(adminRefuse.status).toBe(409)
    expect(await adminRefuse.json()).toMatchObject({ error: { message: '管理员账号不能注销，先在命令行取消管理员' } })

    await fetch(`${base}/__mock/expire-reauth`, { method: 'POST' })
    expect((await send('POST', `/v1/users/${victim.id}/delete`, { reason: '用户申请注销账号' })).status).toBe(403)
    await reauth()
    const done = await send('POST', `/v1/users/${victim.id}/delete`, { reason: '用户申请注销账号' })
    expect(done.status).toBe(200)
    const body = (await done.json()) as { short_id: string; terminated_subscriptions: number }
    expect(body.short_id).toBe(victim.id.slice(0, 8))
    expect(body.terminated_subscriptions).toBe(activeSubs)

    const d = await detail(victim.id)
    expect(d.status).toBe('deleted')
    expect(d.email.endsWith('@deleted.invalid')).toBe(true)
    expect(d.deleted_at).toBeTruthy()

    const again = await send('POST', `/v1/users/${victim.id}/email`, { email: 'gone@example.test', reason: '注销后不应能改邮箱' })
    expect(again.status).toBe(409)
    expect(await again.json()).toMatchObject({ error: { message: '账号已注销' } })
  })
})
