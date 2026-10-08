import { createElement, type ReactElement } from 'react'

// ---------------------------------------------------------------------------
// 二维码编码器（ISO/IEC 18004）：字节模式、纠错 M 级。不引第三方库（运行时依赖只有四个），
// 输出 SVG 元素，不用 data: 也不用内联 style（CSP：img-src 只放行 'self' data:、style-src 'self'）。
//
// 设计稿写的是版本 1–10（M 级最多 213 字节，够订阅链接）；易支付的收银台地址带签名、回跳与中文
// 商品名，常常三四百字节，所以放到 40 版（M 级 2331 字节）。表与算法按标准，单测对照 ISO 附录与
// 公开教程的测试向量，并用内置的解码器把每个版本解回原文。
// ---------------------------------------------------------------------------

export const MIN_VERSION = 1
export const MAX_VERSION = 40

/** M 级每块纠错码字数，下标是版本（0 不用） */
const ECC_PER_BLOCK = [-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28]
/** M 级的纠错块数 */
const BLOCKS = [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49]
/** 纠错等级 M 的格式位（L=01 M=00 Q=11 H=10） */
const EC_LEVEL_BITS = 0

export const sizeOf = (version: number) => version * 4 + 17

/** 一个版本里除功能图形外能放数据与纠错的模块数 */
export function rawDataModules(version: number): number {
  let result = (16 * version + 128) * version + 64
  if (version >= 2) {
    const align = Math.floor(version / 7) + 2
    result -= (25 * align - 10) * align - 55
    if (version >= 7) result -= 36
  }
  return result
}

/** M 级的数据码字数 */
export const dataCodewords = (version: number) => Math.floor(rawDataModules(version) / 8) - ECC_PER_BLOCK[version]! * BLOCKS[version]!

/** 字节模式的字符计数位数：1–9 版 8 位，10–40 版 16 位 */
const countBits = (version: number) => (version <= 9 ? 8 : 16)

/** 字节模式下这个版本最多放多少字节 */
export const byteCapacity = (version: number) => Math.floor((dataCodewords(version) * 8 - 4 - countBits(version)) / 8)

/** 校正图形的中心坐标（行列相同的一组） */
export function alignmentPositions(version: number): number[] {
  if (version === 1) return []
  const count = Math.floor(version / 7) + 2
  const step = Math.floor((version * 8 + count * 3 + 5) / (count * 4 - 4)) * 2
  const result = [6]
  for (let i = count - 1, pos = sizeOf(version) - 7; i >= 1; i--, pos -= step) result[i] = pos
  return result
}

// ---------------------------------------------------------------------------
// GF(2^8) 上的 Reed-Solomon，本原多项式 0x11D
// ---------------------------------------------------------------------------
export function gfMultiply(x: number, y: number): number {
  let z = 0
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d)
    z ^= ((y >>> i) & 1) * x
  }
  return z
}

export function rsDivisor(degree: number): number[] {
  const result: number[] = Array.from({ length: degree }, (_, i) => (i === degree - 1 ? 1 : 0))
  let root = 1
  for (let i = 0; i < degree; i++) {
    for (let j = 0; j < result.length; j++) {
      result[j] = gfMultiply(result[j]!, root)
      if (j + 1 < result.length) result[j]! ^= result[j + 1]!
    }
    root = gfMultiply(root, 0x02)
  }
  return result
}

export function rsRemainder(data: readonly number[], divisor: readonly number[]): number[] {
  const result = divisor.map(() => 0)
  for (const b of data) {
    const factor = b ^ result.shift()!
    result.push(0)
    divisor.forEach((coef, i) => (result[i]! ^= gfMultiply(coef, factor)))
  }
  return result
}

/** 15 位格式信息（含 BCH 与 0x5412 掩码） */
export function formatBits(mask: number): number {
  const data = (EC_LEVEL_BITS << 3) | mask
  let rem = data
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537)
  return ((data << 10) | rem) ^ 0x5412
}

/** 18 位版本信息（7 版起才有） */
export function versionBits(version: number): number {
  let rem = version
  for (let i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1f25)
  return (version << 12) | rem
}

// ---------------------------------------------------------------------------
// 编码
// ---------------------------------------------------------------------------
export interface QrMatrix {
  version: number
  mask: number
  size: number
  /** modules[y][x]：true 为深色 */
  modules: boolean[][]
}

const bit = (x: number, i: number) => ((x >>> i) & 1) !== 0

/** 数据码字：模式 0100、计数、数据、终止符、补齐到字节、0xEC / 0x11 填充 */
export function dataCodewordsFor(bytes: Uint8Array, version: number): number[] {
  const bits: number[] = []
  const push = (value: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((value >>> i) & 1)
  }
  push(0b0100, 4)
  push(bytes.length, countBits(version))
  for (const b of bytes) push(b, 8)
  const capacity = dataCodewords(version) * 8
  push(0, Math.min(4, capacity - bits.length))
  push(0, (8 - (bits.length % 8)) % 8)
  const out: number[] = []
  for (let i = 0; i < bits.length; i += 8) out.push(bits.slice(i, i + 8).reduce((n, b) => (n << 1) | b, 0))
  for (let pad = 0xec; out.length < dataCodewords(version); pad ^= 0xec ^ 0x11) out.push(pad)
  return out
}

/** 分块、算纠错码、交织 */
export function interleave(data: readonly number[], version: number): number[] {
  const numBlocks = BLOCKS[version]!
  const eccLen = ECC_PER_BLOCK[version]!
  const raw = Math.floor(rawDataModules(version) / 8)
  const numShort = numBlocks - (raw % numBlocks)
  const shortLen = Math.floor(raw / numBlocks)
  const divisor = rsDivisor(eccLen)
  const blocks: number[][] = []
  for (let i = 0, k = 0; i < numBlocks; i++) {
    const dat = data.slice(k, k + shortLen - eccLen + (i < numShort ? 0 : 1))
    k += dat.length
    const ecc = rsRemainder(dat, divisor)
    if (i < numShort) dat.push(0)
    blocks.push([...dat, ...ecc])
  }
  const result: number[] = []
  for (let i = 0; i < blocks[0]!.length; i++) {
    blocks.forEach((block, j) => {
      if (i !== shortLen - eccLen || j >= numShort) result.push(block[i]!)
    })
  }
  return result
}

class Grid {
  readonly size: number
  readonly modules: boolean[][]
  readonly fn: boolean[][]
  constructor(readonly version: number) {
    this.size = sizeOf(version)
    this.modules = Array.from({ length: this.size }, () => Array<boolean>(this.size).fill(false))
    this.fn = Array.from({ length: this.size }, () => Array<boolean>(this.size).fill(false))
  }
  set(x: number, y: number, dark: boolean) {
    this.modules[y]![x] = dark
    this.fn[y]![x] = true
  }
  drawFunctionPatterns() {
    const n = this.size
    for (let i = 0; i < n; i++) {
      this.set(6, i, i % 2 === 0)
      this.set(i, 6, i % 2 === 0)
    }
    this.finder(3, 3)
    this.finder(n - 4, 3)
    this.finder(3, n - 4)
    const pos = alignmentPositions(this.version)
    const last = pos.length - 1
    pos.forEach((y, i) =>
      pos.forEach((x, j) => {
        if ((i === 0 && j === 0) || (i === 0 && j === last) || (i === last && j === 0)) return
        for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) this.set(x + dx, y + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1)
      }),
    )
    this.drawFormat(0)
    if (this.version >= 7) {
      const bits = versionBits(this.version)
      for (let i = 0; i < 18; i++) {
        const a = n - 11 + (i % 3)
        const b = Math.floor(i / 3)
        this.set(a, b, bit(bits, i))
        this.set(b, a, bit(bits, i))
      }
    }
  }
  finder(cx: number, cy: number) {
    for (let dy = -4; dy <= 4; dy++) {
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx
        const y = cy + dy
        const dist = Math.max(Math.abs(dx), Math.abs(dy))
        if (x >= 0 && x < this.size && y >= 0 && y < this.size) this.set(x, y, dist !== 2 && dist !== 4)
      }
    }
  }
  drawFormat(mask: number) {
    const bits = formatBits(mask)
    const n = this.size
    for (let i = 0; i <= 5; i++) this.set(8, i, bit(bits, i))
    this.set(8, 7, bit(bits, 6))
    this.set(8, 8, bit(bits, 7))
    this.set(7, 8, bit(bits, 8))
    for (let i = 9; i < 15; i++) this.set(14 - i, 8, bit(bits, i))
    for (let i = 0; i < 8; i++) this.set(n - 1 - i, 8, bit(bits, i))
    for (let i = 8; i < 15; i++) this.set(8, n - 15 + i, bit(bits, i))
    this.set(8, n - 8, true)
  }
  drawCodewords(data: readonly number[]) {
    const n = this.size
    let i = 0
    for (let right = n - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5
      for (let vert = 0; vert < n; vert++) {
        for (let j = 0; j < 2; j++) {
          const x = right - j
          const upward = ((right + 1) & 2) === 0
          const y = upward ? n - 1 - vert : vert
          if (!this.fn[y]![x] && i < data.length * 8) {
            this.modules[y]![x] = bit(data[i >>> 3]!, 7 - (i & 7))
            i++
          }
        }
      }
    }
  }
  applyMask(mask: number) {
    for (let y = 0; y < this.size; y++) {
      for (let x = 0; x < this.size; x++) {
        if (!this.fn[y]![x] && maskHit(mask, x, y)) this.modules[y]![x] = !this.modules[y]![x]
      }
    }
  }
}

export function maskHit(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0
    case 1:
      return y % 2 === 0
    case 2:
      return x % 3 === 0
    case 3:
      return (x + y) % 3 === 0
    case 4:
      return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0
    case 5:
      return ((x * y) % 2) + ((x * y) % 3) === 0
    case 6:
      return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0
    default:
      return (((x + y) % 2) + ((x * y) % 3)) % 2 === 0
  }
}

/** 罚分（标准 4 条）：同色连续、2×2 同色块、类定位图形、深浅比例；选罚分最低的掩码 */
export function penalty(modules: readonly (readonly boolean[])[]): number {
  const n = modules.length
  let result = 0
  const lines = (get: (i: number, j: number) => boolean) => {
    for (let i = 0; i < n; i++) {
      let color = false
      let run = 0
      const history = [0, 0, 0, 0, 0, 0, 0]
      for (let j = 0; j < n; j++) {
        if (get(i, j) === color) {
          run++
          if (run === 5) result += 3
          else if (run > 5) result++
        } else {
          addHistory(run, history, n)
          if (!color) result += countFinderLike(history) * 40
          color = get(i, j)
          run = 1
        }
      }
      if (color) {
        addHistory(run, history, n)
        run = 0
      }
      run += n
      addHistory(run, history, n)
      result += countFinderLike(history) * 40
    }
  }
  lines((y, x) => modules[y]![x]!)
  lines((x, y) => modules[y]![x]!)
  for (let y = 0; y < n - 1; y++) {
    for (let x = 0; x < n - 1; x++) {
      const c = modules[y]![x]
      if (c === modules[y]![x + 1] && c === modules[y + 1]![x] && c === modules[y + 1]![x + 1]) result += 3
    }
  }
  const dark = modules.reduce((sum, row) => sum + row.filter(Boolean).length, 0)
  const total = n * n
  result += (Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1) * 10
  return result
}

function addHistory(run: number, history: number[], size: number) {
  if (history[0] === 0) run += size
  history.pop()
  history.unshift(run)
}

function countFinderLike(h: readonly number[]): number {
  const n = h[1]!
  const core = n > 0 && h[2] === n && h[3] === n * 3 && h[4] === n && h[5] === n
  return (core && h[0]! >= n * 4 && h[6]! >= n ? 1 : 0) + (core && h[6]! >= n * 4 && h[0]! >= n ? 1 : 0)
}

/** 某个版本的功能图形位置（定位、分隔、定时、校正、格式与版本信息）：fn[y][x] 为 true 的不放数据 */
export function functionModules(version: number): boolean[][] {
  const grid = new Grid(version)
  grid.drawFunctionPatterns()
  return grid.fn
}

/** 编码一段文字（UTF-8 字节）；放不下 40 版时返回 null，调用方退回别的办法（复制链接、打开页面） */
export function encodeQr(text: string, maxVersion = MAX_VERSION): QrMatrix | null {
  const bytes = new TextEncoder().encode(text)
  let version = MIN_VERSION
  while (version <= maxVersion && bytes.length > byteCapacity(version)) version++
  if (version > maxVersion) return null
  const codewords = interleave(dataCodewordsFor(bytes, version), version)
  const grid = new Grid(version)
  grid.drawFunctionPatterns()
  grid.drawCodewords(codewords)
  let best = 0
  let bestScore = Number.POSITIVE_INFINITY
  for (let mask = 0; mask < 8; mask++) {
    grid.applyMask(mask)
    grid.drawFormat(mask)
    const score = penalty(grid.modules)
    if (score < bestScore) {
      best = mask
      bestScore = score
    }
    grid.applyMask(mask)
  }
  grid.applyMask(best)
  grid.drawFormat(best)
  return { version, mask: best, size: grid.size, modules: grid.modules }
}

/** 深色模块合成一条 path（每个模块一个 1×1 方块），坐标含 quiet 边距 */
export function qrPath(m: QrMatrix, quiet = 4): string {
  const parts: string[] = []
  m.modules.forEach((row, y) =>
    row.forEach((dark, x) => {
      if (dark) parts.push(`M${x + quiet} ${y + quiet}h1v1h-1z`)
    }),
  )
  return parts.join('')
}

/**
 * 二维码 SVG：白底黑块、四格静区（深色模式下也保持白底，手机才扫得出）。
 * 放不下时返回 null，调用方给替代办法。
 */
export function QrCode({ value, label, className }: { value: string; label: string; className?: string }): ReactElement | null {
  const m = encodeQr(value)
  if (!m) return null
  const span = m.size + 8
  return createElement(
    'svg',
    { viewBox: `0 0 ${span} ${span}`, role: 'img', 'aria-label': label, className, shapeRendering: 'crispEdges', xmlns: 'http://www.w3.org/2000/svg' },
    createElement('rect', { width: span, height: span, fill: '#fff' }),
    createElement('path', { d: qrPath(m), fill: '#000' }),
  )
}
