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
