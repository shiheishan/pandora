import type { MockRoute } from '../types.ts'
import type { User } from './users.ts'

// ---------------------------------------------------------------------------
// 后台改单份订阅的到期时间与流量上限（与 subscription handler/admin_edit.go 同形）
// ---------------------------------------------------------------------------
export function subEditRoutes(store: { users: User[] }): Record<string, MockRoute> {
  void store
  return {}
}
