import type { MockContext, MockRoute } from '../types.ts'
import type { User } from './users.ts'

const reasonOk = (v: unknown) => typeof v === 'string' && [...v.trim()].length >= 5 && [...v.trim()].length <= 500
const EMAIL_RE = /^[^@\s]+@[^@\s]+\.[^@\s]+$/

function findUser(users: User[], ctx: MockContext): User | undefined {
  return users.find((u) => u.id === ctx.params.id)
}

function deletedConflict(u: User, ctx: MockContext): boolean {
  if (u.status === 'deleted') {
    ctx.fail(409, 'conflict', '账号已注销')
    return true
  }
  return false
}

/** 沿 referrer 链向上找，遇到 targetId 表示会形成环 */
function referrerCycle(users: User[], startId: string, targetId: string): boolean {
  let cur: string | null = startId
  for (let i = 0; i < 100 && cur; i++) {
    if (cur === targetId) return true
    const u = users.find((x) => x.id === cur)
    cur = u?.referrer ?? null
  }
  return false
}

// ---------------------------------------------------------------------------
// 后台改邮箱 / 显示名与备注 / 邀请人、注销并匿名化（与 identity handler/admin_edit.go、invite_xb.go 同形）
// ---------------------------------------------------------------------------
export function editRoutes(store: { users: User[] }): Record<string, MockRoute> {
  const { users } = store

  return {
    'POST /v1/users/:id/email': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const u = findUser(users, ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (deletedConflict(u, ctx)) return
      const email = typeof body.email === 'string' ? body.email.trim() : ''
      const fields: Record<string, string> = {}
      if (!EMAIL_RE.test(email)) fields.email = '邮箱格式不正确'
      if (!reasonOk(body.reason)) fields.reason = '请写清原因，5 到 500 个字'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      const taken = users.find((x) => x.id !== u.id && x.email.toLowerCase() === email.toLowerCase())
      if (taken) return ctx.fail(409, 'conflict', '这个邮箱已被别的账号使用')
      u.email = email
      u.email_verified = false
      ctx.send(200, { ok: true, email })
    },

    'POST /v1/users/:id/note': async (ctx) => {
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const u = findUser(users, ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (deletedConflict(u, ctx)) return
      const hasDisplay = 'display_name' in body
      const hasNote = 'admin_note' in body
      if (!hasDisplay && !hasNote) return ctx.fail(422, 'validation_failed', '请求参数校验未通过')
      const fields: Record<string, string> = {}
      if (hasDisplay) {
        const v = body.display_name
        if (v !== null && typeof v !== 'string') fields.display_name = '显示名格式不正确'
        else if (typeof v === 'string' && [...v].length > 64) fields.display_name = '显示名最多 64 字'
      }
      if (hasNote) {
        const v = body.admin_note
        if (v !== null && typeof v !== 'string') fields.admin_note = '备注格式不正确'
        else if (typeof v === 'string' && [...v].length > 2000) fields.admin_note = '备注最多 2000 字'
      }
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (hasDisplay) u.display_name = body.display_name === null || body.display_name === '' ? null : (body.display_name as string)
      if (hasNote) u.admin_note = body.admin_note === null || body.admin_note === '' ? null : (body.admin_note as string)
      ctx.send(200, { ok: true })
    },

    'POST /v1/users/:id/referrer': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      const u = findUser(users, ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (deletedConflict(u, ctx)) return
      const fields: Record<string, string> = {}
      if (!reasonOk(body.reason)) fields.reason = '请写清原因，5 到 500 个字'
      const rid = body.referrer_id
      if (rid !== null && typeof rid !== 'string') fields.referrer_id = '邀请人不存在'
      if (Object.keys(fields).length) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', fields)
      if (rid === null) {
        u.referrer = null
        return ctx.send(200, { ok: true })
      }
      if (rid === u.id) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { referrer_id: '不能把邀请人设成自己' })
      const ref = users.find((x) => x.id === rid)
      if (!ref) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { referrer_id: '邀请人不存在' })
      if (ref.status === 'deleted') return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { referrer_id: '邀请人已注销' })
      if (referrerCycle(users, rid, u.id)) {
        return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { referrer_id: '不能设成他自己邀请来的人（含下级的下级）' })
      }
      u.referrer = rid
      ctx.send(200, { ok: true })
    },

    'POST /v1/users/:id/delete': async (ctx) => {
      if (!ctx.requireReauth()) return
      const body = await ctx.body()
      if (!body) return ctx.fail(400, 'bad_request', '请求体不是合法的 JSON')
      if (ctx.params.id === ctx.user.userId) return ctx.fail(409, 'conflict', '不能注销自己的账号')
      const u = findUser(users, ctx)
      if (!u) return ctx.fail(404, 'not_found', '资源不存在或无权访问')
      if (u.status === 'deleted') return ctx.fail(409, 'conflict', '账号已注销')
      if (u.is_admin) return ctx.fail(409, 'conflict', '管理员账号不能注销，先在命令行取消管理员')
      if (!reasonOk(body.reason)) return ctx.fail(422, 'validation_failed', '请求参数校验未通过', { reason: '请写清原因，5 到 500 个字' })
      const now = new Date().toISOString()
      let n = 0
      for (const s of u.subs) {
        if (s.status === 'active') {
          s.status = 'expired'
          s.current_period_end = now
          n++
        }
      }
      u.email = `deleted-${u.id.replaceAll('-', '')}@deleted.invalid`
      u.display_name = null
      u.admin_note = null
      u.telegram = null
      u.email_verified = false
      u.status = 'deleted'
      u.deleted_at = now
      ctx.send(200, { ok: true, short_id: u.id.slice(0, 8), terminated_subscriptions: n })
    },
  }
}
