export const GiB = 1024 ** 3
export const QUOTA_MAX_BYTES = 10 * 1024 ** 4

const PERIOD_END_MSG = '到期时间须晚于现在，且不超过 10 年'
const LIMIT_MSG = '流量上限须在 0 到 10 TiB 之间，留空表示不限'

/** datetime-local 的值（本地时间 'YYYY-MM-DDTHH:mm'）→ RFC3339（toISOString）；不合法返回 null */
export function localToRFC3339(v: string): string | null {
  const text = v.trim()
  if (!text) return null
  const d = new Date(text)
  if (Number.isNaN(d.getTime())) return null
  return d.toISOString()
}

/** ISO 时间 → datetime-local 的值（本地时间，到分钟） */
export function isoToLocalInput(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 到期时间的前端检查：晚于 now、不超过 now + 10 年；通过返回 null，否则返回文案（同后端） */
export function periodEndProblem(iso: string, now: Date): string | null {
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return PERIOD_END_MSG
  const end = t
  const nowMs = now.getTime()
  const maxMs = new Date(now).setFullYear(now.getFullYear() + 10)
  if (!(end > nowMs) || end > maxMs) return PERIOD_END_MSG
  return null
}

/** 流量上限输入（GiB，可带小数）→ 字节；空串 → null（不限）；不合法或越界 → { error } */
export function parseLimitGiB(v: string): { bytes: number | null } | { error: string } {
  const text = v.trim()
  if (text === '') return { bytes: null }
  if (!/^\d+(\.\d+)?$/.test(text)) return { error: LIMIT_MSG }
  const gib = Number(text)
  if (!Number.isFinite(gib) || gib < 0) return { error: LIMIT_MSG }
  const bytes = Math.round(gib * GiB)
  if (!Number.isSafeInteger(bytes) || bytes > QUOTA_MAX_BYTES) return { error: LIMIT_MSG }
  return { bytes }
}
