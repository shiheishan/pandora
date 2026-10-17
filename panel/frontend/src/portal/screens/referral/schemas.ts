import { z } from 'zod'

// 邀请页的响应 schema：只依赖 zod，页面与假后端测试共用

// ---------------------------------------------------------------------------
// GET v1/me/invite：没有活动邀请码时后端懒生成一个（会写库的 GET，重复请求拿到同一个码）
// invitees 最多 100 条、按 bound_at 倒序；risk_flag 是风控内部判定，schema 收下但页面不展示
// ---------------------------------------------------------------------------
export const inviteCodeSchema = z.object({
  code: z.string(),
  status: z.enum(['active', 'exhausted', 'disabled']),
  invited: z.number().int(),
  visits: z.number().int(),
  max_uses: z.number().int().nullable(),
  created_at: z.string(),
})
export type InviteCode = z.output<typeof inviteCodeSchema>

export const inviteSchema = z.object({
  invite: z.object({ code: z.string(), invited: z.number().int(), max_uses: z.number().int().nullable(), created_at: z.string() }),
  invitees: z.array(z.object({ email: z.string(), bound_at: z.string(), risk_flag: z.enum(['none', 'suspicious', 'confirmed_fraud']) })),
  codes: z.array(inviteCodeSchema),
  max_codes: z.number().int(),
})
export type Invite = z.output<typeof inviteSchema>
