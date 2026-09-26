/**
 * [INPUT]: 依赖 ../../../core/format 的 formatBytes / formatCount / formatDateTime，依赖 ../../../ui 的 QueryView / Tag，依赖 ./logic 的 bandwidthBuckets / heartbeatLabel / ruleSummary，依赖 ./queries 的 useNodeMetrics / useNodeRouting，依赖 ./schemas 的 NodeRow，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeMonitor（节点抽屉「监控」标签）
 * [POS]: admin/screens/nodes 抽屉的监控页（设计稿 d_metrics）：四个 KPI（在线用户悬停看在线 IP、CPU、内存、24h 流量）、近 24 小时带宽柱（GET v1/nodes/{id}/metrics?minutes=1440 按整点分桶，手写，不引图表库）、运行信息（契约待补·前端：health_score、applied/desired 配置版本、agent_version、资源池、授权套餐）、单节点路由只读摘要。节点自身没有探针时 CPU / 内存回退到列表里所在服务器的数据
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatBytes, formatCount, formatDateTime } from '../../../core/format'
import { QueryView, Tag } from '../../../ui'
import { bandwidthBuckets, heartbeatLabel, ruleSummary } from './logic'
import css from './nodes.module.css'
import { useNodeMetrics, useNodeRouting } from './queries'
import type { NodeRow } from './schemas'

const pct = (v: number | null | undefined) => (v == null ? '—' : `${Math.round(v)}%`)

export function NodeMonitor({ node }: { node: NodeRow }) {
  const metrics = useNodeMetrics(node.id)
  const routing = useNodeRouting(node.id)
  const latest = metrics.data?.latest
  const cpu = latest ? latest.cpu_percent : node.cpu_percent
  const mem = latest && latest.mem_total_mb ? (latest.mem_used_mb / latest.mem_total_mb) * 100 : node.mem_percent
  const buckets = bandwidthBuckets(metrics.data?.points ?? [])
  const peak = Math.max(1, ...buckets.map((b) => b.mbps ?? 0))

  return (
    <div className={css.stackLg}>
      <div className={css.kpis}>
        <div className={css.kpi} title={`在线 IP ${node.online_ips}（明显多于在线用户说明有人共享账号）`}>
          <div className={css.kpiLabel}>在线用户</div>
          <div className={css.kpiValue}>{formatCount(node.online_users)}</div>
          <div className={css.faint}>在线 IP {formatCount(node.online_ips)}</div>
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>CPU</div>
          <div className={css.kpiValue}>{pct(cpu)}</div>
          {!latest && node.metrics_at && <div className={css.faint}>所在服务器</div>}
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>内存</div>
          <div className={css.kpiValue}>{pct(mem)}</div>
          {latest && (
            <div className={css.faint}>
              {latest.mem_used_mb} / {latest.mem_total_mb} MB
            </div>
          )}
        </div>
        <div className={css.kpi}>
          <div className={css.kpiLabel}>24h 流量</div>
          <div className={css.kpiValue}>{formatBytes(node.traffic_bytes_24h)}</div>
        </div>
      </div>

      <section>
        <div className={css.groupLabel}>带宽 · 近 24 小时（Mbps）</div>
        <QueryView query={metrics} rows={1} isEmpty={(d) => d.points.length === 0} empty={<div className={css.faint}>还没有探针数据：节点从未心跳，或探针挂在服务器的控制节点上。</div>}>
          {() => (
            <div className={css.bars} role="img" aria-label={`近 24 小时带宽，峰值 ${peak} Mbps`}>
              {buckets.map((b, i) => (
                <span key={i} className={css.bar} title={`${b.hour}:00 · ${b.mbps == null ? '无数据' : `${b.mbps} Mbps`}`}>
                  <span className={b.mbps == null ? css.barEmpty : css.barFill} style={{ height: `${b.mbps == null ? 4 : Math.max(4, (b.mbps / peak) * 100)}%` }} />
                </span>
              ))}
            </div>
          )}
        </QueryView>
      </section>

      <section>
        <div className={css.groupLabel}>运行信息</div>
        <dl className={css.facts}>
          <dt>心跳</dt>
          <dd>{node.last_heartbeat_at ? `${heartbeatLabel(node.last_heartbeat_at)} · ${formatDateTime(node.last_heartbeat_at)}` : '从未心跳'}</dd>
          <dt>健康分</dt>
          <dd>{node.health_score ?? '—'}</dd>
          <dt>配置版本</dt>
          <dd>
            已应用 {node.applied_config_version ?? '—'} / 期望 {node.desired_config_version ?? '—'}
            {node.applied_config_version !== node.desired_config_version && node.desired_config_version !== null && (
              <Tag tone="warn" className={css.inlineTag}>
                未同步
              </Tag>
            )}
          </dd>
          <dt>Agent</dt>
          <dd>{node.agent_version ?? '—'}</dd>
          <dt>资源池</dt>
          <dd>{node.pool_name ?? '未加入'}</dd>
          <dt>授权套餐</dt>
          <dd>{node.granted_plans.length ? node.granted_plans.join('、') : '没有套餐能用到这个节点'}</dd>
        </dl>
      </section>

      <section>
        <div className={css.groupLabel}>单节点路由（覆盖全局）</div>
        <QueryView query={routing} rows={1} isEmpty={(d) => d.routes.length === 0} empty={<div className={css.faint}>沿用全局规则</div>}>
          {(d) => (
            <ul className={css.ruleList}>
              {d.routes.map((r, i) => (
                <li key={i} className={r.enabled ? undefined : css.ruleOff}>
                  <span>{ruleSummary(r)}</span>
                  <span className={css.mono}>→ {r.outbound_tag}</span>
                </li>
              ))}
            </ul>
          )}
        </QueryView>
      </section>
    </div>
  )
}
