import { z } from 'zod'

// PATCH v1/me/subscriptions/{id} 的响应（纯 zod，tests/ 的假后端测试也直接用它）
export const renameSchema = z.object({ label: z.string().nullable(), client_name: z.string() })
