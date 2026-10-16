/** 路由页出站行的地址列：从出站 settings 里取 server 与端口 */

/** 端口：server_port 优先，缺了退回 port；数字或数字字符串才算 */
function outboundPort(settings: Record<string, unknown>): number | string | null {
  for (const key of ['server_port', 'port']) {
    const v = settings[key]
    if (typeof v === 'number' && Number.isFinite(v)) return v
    if (typeof v === 'string' && v.trim() !== '' && Number.isFinite(Number(v))) return v
  }
  return null
}

/**
 * 出站的地址列：settings 里的 server 与 server_port（缺 server_port 时退回 port）；没有 server 时返回 '—'。
 * settings 不是对象（含 JSON 解析失败后传进来的非对象值）→ '—'；IPv6（含冒号）写成 [server]:port
 */
export function outboundAddress(settings: unknown): string {
  if (!settings || typeof settings !== 'object' || Array.isArray(settings)) return '—'
  const server = (settings as Record<string, unknown>).server
  if (typeof server !== 'string' || server.trim() === '') return '—'
  const port = outboundPort(settings as Record<string, unknown>)
  const host = server.includes(':') ? `[${server}]` : server
  return port === null ? host : `${host}:${port}`
}
