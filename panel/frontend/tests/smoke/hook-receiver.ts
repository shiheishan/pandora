/**
 * [INPUT]: 依赖 node:http 与 node:fs；命令行参数 <状态目录> <端口>
 * [OUTPUT]: 本机插件钩子接收端：只听 127.0.0.1，每收到一次投递就往 <状态目录>/hook-received.jsonl 追加一行（事件、投递 id、签名头是否齐全、正文长度），回 200；GET /ready 回 200 供启动探测
 * [POS]: tests/smoke 的插件投递夹具，由 seed.ts 以独立进程拉起并把 pid 记进状态目录的 pids（run-smoke-stack.sh down 一并收掉）；只在非生产环境成立——devMode 下面板本来就放行回环地址，这里不改 Go、不放宽任何校验
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { appendFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { join } from 'node:path'

const [stateDir, portText] = process.argv.slice(2)
if (!stateDir || !portText) {
  console.error('用法: node hook-receiver.ts <状态目录> <端口>')
  process.exit(2)
}
const out = join(stateDir, 'hook-received.jsonl')

createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/ready') {
    res.end('ok')
    return
  }
  let size = 0
  req.on('data', (chunk: Buffer) => (size += chunk.length))
  req.on('end', () => {
    const h = req.headers
    appendFileSync(
      out,
      JSON.stringify({
        event: h['x-pandora-event'] ?? null,
        delivery: h['x-pandora-delivery'] ?? null,
        signed: typeof h['x-pandora-signature'] === 'string' && typeof h['x-pandora-timestamp'] === 'string',
        bytes: size,
        at: new Date().toISOString(),
      }) + '\n',
    )
    res.end('ok')
  })
}).listen(Number(portText), '127.0.0.1')
