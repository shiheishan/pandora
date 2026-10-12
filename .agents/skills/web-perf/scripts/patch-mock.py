#!/usr/bin/env python3
"""给 scratchpad 里的前端拷贝打性能测试补丁（只改拷贝，绝不改仓库里的 panel/frontend）。

用法：patch-mock.py <前端拷贝目录>（重复执行是安全的：已打过就跳过）

打完之后假后端认这几个环境变量（只有 admin 入口生效）：
  PERF_NODES=N          节点补足到 N 个（按前 3 个节点的协议配置轮换，名字形如「HK 节点 0123」）
  PERF_TRIGGER=old      每次模拟心跳都推一条 nodes.changed（迁移 00110 之前的触发器：心跳也广播）
  PERF_TRIGGER=new      模拟心跳只在距上次心跳 >= 90 秒时推（00110 之后：只有在线状态翻转才广播）
  PERF_TRIGGER=none     心跳从不推事件（纯基线）
  PERF_NODES_EVENT_MS=M 另外每 M 毫秒固定推一条 nodes.changed（强制按固定节奏重拉，测单次刷新耗时）
心跳模拟：每 2 秒按顺序轮到 66 个节点（1000 个节点时每个约 30 秒一次，与原生节点的心跳间隔一致）。

另外让 `vite preview` 也挂上假后端（原本只在 dev server 挂），这样能测生产构建的产物。
锚点对不上时直接报错退出：说明假后端改过了，按报错位置手工对一下再更新本脚本。
"""
import re
import sys

MARK = "// PERF（web-perf skill 补丁，只在 scratchpad 的拷贝里）"


def patch_nodes(fe):
    p = fe + "/dev/mock/admin/nodes.ts"
    s = open(p, encoding="utf-8").read()
    if MARK in s:
        return False
    # 锚点：节点列表初始化完成之后、给第一个节点挂路由规则的那一行
    m = re.search(r"^store\[0\]!\.routing = .*\n", s, re.M)
    if not m:
        sys.exit(f"{p}: 找不到锚点 store[0]!.routing = …")
    block = MARK + r'''
{
  const target = Number(process.env.PERF_NODES || 0)
  const mode = process.env.PERF_TRIGGER || 'old'
  const types = ['vless', 'trojan', 'shadowsocks', 'hysteria2', 'vmess', 'tuic']
  const ccs = ['HK', 'JP', 'SG', 'US', 'KR', 'TW', 'DE', 'GB']
  const tpl = store.slice(0, 3)
  for (let i = store.length; i < target; i++) {
    const t = tpl[i % tpl.length]!
    store.push(node({ name: `${ccs[i % ccs.length]} 节点 ${String(i).padStart(4, '0')}`, node_type: types[i % types.length]!, serving_status: i % 17 === 0 ? 'disabled' : 'active', country_code: ccs[i % ccs.length]!, online_users: (i * 37) % 500, online_ips: (i * 41) % 600, traffic_bytes_24h: ((i * 7919) % 2000) * GB, protocol_config: JSON.parse(JSON.stringify(t.protocol_config)) as Json }, i % 6))
  }
  const g = globalThis as unknown as { __perfNodeEvents?: Set<(id: string) => void> }
  g.__perfNodeEvents ??= new Set()
  let cursor = 0
  if (target) setInterval(() => {
    for (let k = 0; k < 66; k++) {
      const n = store[cursor++ % store.length]!
      const prev = n.last_heartbeat_at ? new Date(n.last_heartbeat_at).getTime() : null
      n.last_heartbeat_at = new Date().toISOString(); n.online_users = Math.floor(Math.random() * 500); n.traffic_bytes_24h += 1024 * 1024
      const notify = mode === 'old' || (mode === 'new' && (prev === null || Date.now() - prev >= 90_000))
      if (notify) g.__perfNodeEvents!.forEach((f) => f(n.id))
    }
  }, 2000).unref()
}
'''
    s = s[: m.end()] + block + s[m.end():]
    open(p, "w", encoding="utf-8").write(s)
    return True


def patch_mock_api(fe):
    p = fe + "/dev/mock-api.ts"
    s = open(p, encoding="utf-8").read()
    if MARK in s:
        return False
    # SSE：在保活定时器之后挂两路 nodes.changed（固定节奏 + 模拟心跳），断开时一并清理
    old_ping = "    const ping = setInterval(() => res.write(': ping\\n\\n'), 25_000)\n"
    if old_ping not in s:
        sys.exit(f"{p}: 找不到 SSE 保活定时器 const ping = setInterval(…)")
    s = s.replace(old_ping, old_ping + "    " + MARK + r'''
    const perfMs = Number(process.env.PERF_NODES_EVENT_MS || 0)
    const perf = app === 'admin' && perfMs ? setInterval(() => res.write(`id: ${++seq}\nevent: nodes.changed\ndata: ${JSON.stringify({ table: 'nodes', op: 'UPDATE', id: randomUUID() })}\n\n`), perfMs) : null
    const gp = globalThis as unknown as { __perfNodeEvents?: Set<(id: string) => void> }
    const onNode = (id: string) => { if (app === 'admin') res.write(`id: ${++seq}\nevent: nodes.changed\ndata: ${JSON.stringify({ table: 'nodes', op: 'UPDATE', id })}\n\n`) }
    gp.__perfNodeEvents?.add(onNode)
''')
    old_close = "      clearInterval(ping)\n"
    if old_close not in s:
        sys.exit(f"{p}: 找不到 SSE 断开时的 clearInterval(ping)")
    s = s.replace(old_close, old_close + "      if (perf) clearInterval(perf)\n      gp.__perfNodeEvents?.delete(onNode)\n", 1)
    # 插件：configureServer 的函数体抽成 mount，同时挂到 configurePreviewServer
    m = re.search(r"  return \{\n    name: 'pandora-mock-api',\n    apply: 'serve',\n    configureServer\(server\) \{\n(.*?)\n    \},\n  \}\n\}\n", s, re.S)
    if not m:
        sys.exit(f"{p}: 找不到 mockApi 插件的 return {{ name: 'pandora-mock-api', apply: 'serve', configureServer(server) {{ … }} }}")
    s = s.replace(m.group(0), "  // eslint-disable-next-line @typescript-eslint/no-explicit-any\n  const mount = (server: any) => {\n" + m.group(1)
                  + "\n  }\n  return {\n    name: 'pandora-mock-api',\n    configurePreviewServer: mount,\n    configureServer: mount,\n  }\n}\n")
    open(p, "w", encoding="utf-8").write(s)
    return True


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    fe = sys.argv[1].rstrip("/")
    a, b = patch_nodes(fe), patch_mock_api(fe)
    print(("已打补丁: " if a or b else "已是打过补丁的拷贝: ") + fe)


if __name__ == "__main__":
    main()
