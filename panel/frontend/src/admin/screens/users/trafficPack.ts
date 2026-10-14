// 后台加流量包（billing.GrantTrafficPackAsAdmin）的前端口径：按 GB 填，换成字节提交

const GIB = 1024 ** 3

/** 一次最多 10240 GB（后端上限 10 TiB，fields.bytes） */
export const GRANT_GB_MAX = 10240
export const GRANT_GB_PRESETS = [10, 50, 100] as const

/** GB 数：1 到 10240 的整数，换成字节；否则 null */
export function parseGrantGB(input: string): number | null {
  const v = input.trim()
  if (!/^\d{1,5}$/.test(v)) return null
  const n = Number(v)
  return n >= 1 && n <= GRANT_GB_MAX ? n * GIB : null
}

/** 原因 5–500 字（后端 fields.reason） */
export function grantReasonProblem(reason: string): string | null {
  const n = [...reason.trim()].length
  if (n < 5) return '请写清加流量的原因，至少 5 个字'
  if (n > 500) return '原因不超过 500 个字'
  return null
}
