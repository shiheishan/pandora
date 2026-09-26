/**
 * [INPUT]: 依赖浏览器 URL.createObjectURL / document.createElement（可注入，便于测试）
 * [OUTPUT]: 对外提供 saveFile、filenameFromDisposition、toCsv
 * [POS]: core 的文件下载原语：导出接口经 api.requestRaw 拿到 Response 后由这里存成文件；前端自己拼的 CSV（如批量生成的优惠码）也走这里。blob: 地址只用于 <a download>，不涉及 CSP 的 script-src / style-src
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */

interface DownloadEnv {
  createObjectURL(blob: Blob): string
  revokeObjectURL(url: string): void
  click(href: string, filename: string): void
}

const browserEnv = (): DownloadEnv => ({
  createObjectURL: (blob) => URL.createObjectURL(blob),
  revokeObjectURL: (url) => URL.revokeObjectURL(url),
  click(href, filename) {
    const a = document.createElement('a')
    a.href = href
    a.download = filename
    a.rel = 'noopener'
    document.body.append(a)
    a.click()
    a.remove()
  },
})

/** 把 Blob 存成文件；对象 URL 在下一轮事件循环后回收（Safari 需要点击后仍可读）。 */
export function saveFile(blob: Blob, filename: string, env: DownloadEnv = browserEnv()): void {
  const url = env.createObjectURL(blob)
  env.click(url, filename)
  setTimeout(() => env.revokeObjectURL(url), 0)
}

/**
 * 从 Content-Disposition 取文件名；缺失（如幂等重放不带这个头）时返回 fallback。
 * 只认 filename="…" 与 filename=…，不处理 RFC 5987 的 filename*。
 */
export function filenameFromDisposition(header: string | null, fallback: string): string {
  const match = header ? /filename="?([^";]+)"?/i.exec(header) : null
  const name = match?.[1]?.trim()
  return name ? name : fallback
}

/**
 * 拼 CSV：带 UTF-8 BOM（Excel 打开中文不乱码），每格按 RFC 4180 转义；
 * 以 = + - @ 开头的格前加 '，防止被表格软件当公式执行。
 */
export function toCsv(rows: ReadonlyArray<ReadonlyArray<string | number>>): Blob {
  const cell = (v: string | number) => {
    let s = String(v)
    if (/^[=+\-@]/.test(s)) s = `'${s}`
    return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const text = rows.map((r) => r.map(cell).join(',')).join('\r\n')
  return new Blob(['﻿', text, '\r\n'], { type: 'text/csv;charset=utf-8' })
}
