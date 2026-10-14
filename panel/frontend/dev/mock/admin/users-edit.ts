import type { MockRoute } from '../types.ts'
import type { User } from './users.ts'

// ---------------------------------------------------------------------------
// 后台改邮箱 / 显示名与备注 / 邀请人、注销并匿名化（与 identity handler/admin_edit.go、invite_xb.go 同形）
// ---------------------------------------------------------------------------
export function editRoutes(store: { users: User[] }): Record<string, MockRoute> {
  void store
  return {}
}
