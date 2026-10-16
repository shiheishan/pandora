// ---------------------------------------------------------------------------
// 自绘下拉框（Select）的纯逻辑：键盘移动、首字母跳转、列表定位。不碰 DOM。
// ---------------------------------------------------------------------------
export interface Opt {
  value: string
  label: string
  disabled?: boolean
}

type MoveKey = 'ArrowDown' | 'ArrowUp' | 'Home' | 'End' | 'PageDown' | 'PageUp'

const PAGE = 10

/** 方向键 / Home / End / PageUp / PageDown 后的活动项下标；跳过禁用项，到头不循环（停在首 / 末可用项）；没有可用项回 -1 */
export function moveActive(options: readonly Opt[], from: number, key: MoveKey): number {
  const enabled = options.flatMap((o, i) => (o.disabled ? [] : [i]))
  if (enabled.length === 0) return -1
  const first = enabled[0]!
  const last = enabled[enabled.length - 1]!
  if (key === 'Home') return first
  if (key === 'End') return last
  const down = key === 'ArrowDown' || key === 'PageDown'
  const steps = key === 'PageDown' || key === 'PageUp' ? PAGE : 1
  // 按方向数第 steps 个可用项；前面不够就停在那一头
  const ahead = down ? enabled.filter((i) => i > from) : enabled.filter((i) => i < from).reverse()
  if (ahead.length === 0) return down ? last : first
  return ahead[Math.min(steps, ahead.length) - 1]!
}

/**
 * 首字母跳转：buffer 是 500ms 内累积的已输入字符；从 from 的下一项起循环找 label 以 buffer 开头（不分大小写）的可用项；
 * buffer 由同一个字符重复组成时（如 "bbb"）按单字符在匹配项之间轮转；找不到回 from
 */
export function typeahead(options: readonly Opt[], from: number, buffer: string): number {
  const n = options.length
  if (n === 0 || !buffer) return from
  const chars = [...buffer.toLowerCase()]
  const needle = chars.every((c) => c === chars[0]) ? chars[0]! : chars.join('')
  for (let step = 1; step <= n; step++) {
    const idx = (((from + step) % n) + n) % n
    const opt = options[idx]!
    if (!opt.disabled && opt.label.toLowerCase().startsWith(needle)) return idx
  }
  return from
}

/** 列表位置（照设计稿 openMenu）：rect 是触发器的 getBoundingClientRect，count 是选项数；下方放不下且上方更宽时往上翻 */
export function placeList(
  rect: { top: number; bottom: number; left: number; width: number },
  viewportHeight: number,
  count: number,
): { top: number; left: number; width: number; maxHeight: number; up: boolean } {
  const h = Math.min(280, count * 33 + 10)
  const below = viewportHeight - rect.bottom
  const up = below < h + 8 && rect.top > below
  return { top: up ? Math.max(8, rect.top - h - 4) : rect.bottom + 4, left: rect.left, width: Math.max(rect.width, 140), maxHeight: 280, up }
}
