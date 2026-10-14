import { formatBytes } from '../../../core/format'
import type { NodeTraffic, RateRange } from './schemas'

// 节点补齐 Xboard（迁移 00045）的纯逻辑：标签、流量上限与重置日、时段倍率。
// 上限与文案照后端 node/service 的 node_xb_fields.go、traffic_limit.go、relays.go、nodes_batch.go，后端改了这里一起改。

export const NODE_TAGS_MAX = 10
export const NODE_TAG_MAX_CHARS = 16
export const NODE_RATE_SCHEDULE_MAX = 12
export const NODE_RESET_DAY_MAX = 31
export const NODE_BATCH_EDIT_MAX = 100
export const RELAYS_PER_NODE_MAX = 20

const GIB = 1024 ** 3

/** 标签输入框的文本拆成标签：逗号（中英文）、顿号、换行分隔，去掉首尾空白与空项 */
export function splitTags(text: string): string[] {
  return text
    .split(/[,，、\n]/)
    .map((t) => t.trim())
    .filter((t) => t !== '')
}

/** 后端 NormalizeNodeTags 的去重口径：忽略大小写，留第一次出现的写法 */
export function dedupeTags(tags: readonly string[]): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const t of tags) {
    const k = t.trim().toLowerCase()
    if (k === '' || seen.has(k)) continue
    seen.add(k)
    out.push(t.trim())
  }
  return out
}

// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/

/** 与后端 NormalizeNodeTags 同一套规则与文案；合法回 null */
export function tagsError(tags: readonly string[]): string | null {
  for (const [i, raw] of tags.entries()) {
    const t = raw.trim()
    if (t === '') return `第 ${i + 1} 个标签不能为空`
    if (CONTROL.test(t)) return `第 ${i + 1} 个标签含有控制字符`
    if ([...t].length > NODE_TAG_MAX_CHARS) return `第 ${i + 1} 个标签最多 ${NODE_TAG_MAX_CHARS} 个字`
  }
  if (dedupeTags(tags).length > NODE_TAGS_MAX) return `最多 ${NODE_TAGS_MAX} 个标签`
  return null
}

/** 流量上限（字节）→ 输入框里的 GiB 文本；不限为空串 */
export function limitToGiBText(bytes: number | null): string {
  if (bytes === null) return ''
  return String(Math.round((bytes / GIB) * 100) / 100)
}

/** 输入框的 GiB 文本 → 字节：空串 = 不限（null）；不是正数回 'invalid' */
export function parseLimitGiB(text: string): number | null | 'invalid' {
  const t = text.trim()
  if (t === '') return null
  const n = Number(t)
  if (!Number.isFinite(n) || n <= 0) return 'invalid'
  const bytes = Math.round(n * GIB)
  return bytes >= 1 && Number.isSafeInteger(bytes) ? bytes : 'invalid'
}

/** 重置日输入：空串 = 不自动重置（null）；1–31 的整数，其余 'invalid'（短月取月末，后端口径） */
export function parseResetDay(text: string): number | null | 'invalid' {
  const t = text.trim()
  if (t === '') return null
  if (!/^\d{1,2}$/.test(t)) return 'invalid'
  const n = Number(t)
  return n >= 1 && n <= NODE_RESET_DAY_MAX ? n : 'invalid'
}

function hhmm(s: string, end: boolean): number | null {
  if (end && s === '24:00') return 1440
  const m = /^(\d{2}):(\d{2})$/.exec(s)
  if (!m) return null
  const h = Number(m[1])
  const min = Number(m[2])
  if (h > 23 || min > 59) return null
  const v = h * 60 + min
  return end && v === 0 ? null : v
}

const rateOK = (r: number) => Number.isFinite(r) && r >= 0 && r <= 9999.99 && Math.round(r * 100) / 100 === r

/** 时段倍率的校验，规则与文案照后端 ParseRateSchedule（时段按站点时区，不许重叠，跨零点拆两段）；合法回 null */
export function scheduleError(ranges: readonly RateRange[]): string | null {
  if (ranges.length > NODE_RATE_SCHEDULE_MAX) return `最多 ${NODE_RATE_SCHEDULE_MAX} 段`
  const parsed: Array<{ start: number; end: number }> = []
  for (const [i, r] of ranges.entries()) {
    const start = hhmm(r.start, false)
    const end = hhmm(r.end, true)
    if (start === null || end === null) return `第 ${i + 1} 段：时间要写成 HH:MM（如 08:00），结束可写 24:00`
    if (start >= end) return `第 ${i + 1} 段：开始要早于结束；跨零点请拆成两段（如 22:00–24:00 与 00:00–06:00）`
    if (!rateOK(r.rate)) return `第 ${i + 1} 段：倍率要在 0 到 9999.99 之间，最多两位小数`
    parsed.push({ start, end })
  }
  parsed.sort((a, b) => a.start - b.start)
  for (let i = 1; i < parsed.length; i++) {
    if (parsed[i]!.start < parsed[i - 1]!.end) return `第 ${i + 1} 段与第 ${i} 段时间重叠`
  }
  return null
}

/** 列表与设置页的用量：「已用 / 上限」，不限时只显示已用 */
export function trafficUsageLabel(t: NodeTraffic): string {
  return t.limit_bytes === null ? formatBytes(t.used_bytes) : `${formatBytes(t.used_bytes)} / ${formatBytes(t.limit_bytes)}`
}

/** 用量占上限的百分比（0–100，四舍五入）；不限为 null */
export function trafficPercent(t: NodeTraffic): number | null {
  if (t.limit_bytes === null || t.limit_bytes <= 0) return null
  return Math.min(100, Math.round((t.used_bytes / t.limit_bytes) * 100))
}
