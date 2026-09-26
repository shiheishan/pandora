/**
 * [INPUT]: 依赖 ../../../core/format 的 formatDateTime / relativeTime，依赖 ../../../core/router 的 href，依赖 ../../../ui 的 Empty / QueryView / Table / Tag，依赖 ./api 的 useUserProfile / UserProfile，依赖 ./Users.module.css
 * [OUTPUT]: 对外提供 RiskTab 与 sharingHint
 * [POS]: 用户抽屉「风控」标签（待补·前端，只在持 security.audit.read 时出现）：GET v1/users/{id}/profile 的疑似分享提示（fetch_sources_7d）、IP 聚合（accounts > 1 高亮）、共用 IP 的关联账号（可点进对方抽屉）、订阅拉取记录与最近安全事件。明文 IP 与 UA 只在这里出现
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { formatDateTime, relativeTime } from '../../../core/format'
import { href } from '../../../core/router'
import { Empty, QueryView, Table, Tag } from '../../../ui'
import { useUserProfile, type UserProfile } from './api'
import css from './Users.module.css'

/** 近 7 天不同来源数的提示：3 个以上值得看一眼，6 个以上多半是分享（仅线索，学校公司共用出口很常见） */
export function sharingHint(sources: number): { text: string; tone: 'ok' | 'warn' | 'danger' } {
  if (sources >= 6) return { text: `近 7 天 ${sources} 个不同来源拉取订阅，疑似分享`, tone: 'danger' }
  if (sources >= 3) return { text: `近 7 天 ${sources} 个不同来源拉取订阅，留意是否分享`, tone: 'warn' }
  return { text: `近 7 天 ${sources} 个来源拉取订阅`, tone: 'ok' }
}

export function RiskTab({ userId, now }: { userId: string; now: Date }) {
  const q = useUserProfile(userId, true)
  return (
    <QueryView query={q} rows={4} isEmpty={() => false} empty={null}>
      {(p) => <Profile p={p} now={now} />}
    </QueryView>
  )
}

function Profile({ p, now }: { p: UserProfile; now: Date }) {
  const hint = sharingHint(p.fetch_sources_7d)
  return (
    <>
      <p className={`${css.notice} ${css[`notice_${hint.tone}`]}`}>
        {hint.text}
        {p.registered_ip && <> · 注册 IP <span className={css.mono}>{p.registered_ip}</span></>}
      </p>

      <h3 className={css.sectionTitle}>登录与操作 IP</h3>
      <Table
        label="IP 聚合"
        columns={[
          { key: 'ip', header: 'IP', mono: true, render: (r) => <span className={r.accounts > 1 ? css.tone_warn : undefined}>{r.ip || '（无法解密）'}</span> },
          { key: 'count', header: '次数', align: 'right', width: '56px', render: (r) => r.count },
          { key: 'accounts', header: '同 IP 账号', align: 'right', width: '84px', render: (r) => (r.accounts > 1 ? <Tag tone="warn">{r.accounts}</Tag> : r.accounts) },
          { key: 'last', header: '最近', align: 'right', width: '84px', render: (r) => <span title={formatDateTime(r.last)}>{relativeTime(r.last, now)}</span> },
        ]}
        rows={p.ips}
        rowKey={(r) => r.ip + r.first}
        empty={<Empty bare title="没有 IP 记录" description="用户登录或操作后会记录来源 IP。" />}
      />

      <h3 className={css.sectionTitle}>共用 IP 的其他账号</h3>
      {p.related.length === 0 ? (
        <p className={css.small}>没有发现与其他账号共用 IP。</p>
      ) : (
        <ul className={css.related}>
          {p.related.map((r) => (
            <li key={r.id}>
              <a className={css.link} href={href(`/users/list/${encodeURIComponent(r.id)}/risk`)}>
                {r.email}
              </a>
            </li>
          ))}
        </ul>
      )}

      <h3 className={css.sectionTitle}>订阅拉取（最近 50 次）</h3>
      <Table
        label="订阅拉取记录"
        columns={[
          { key: 'at', header: '时间', width: '84px', render: (f) => <span title={formatDateTime(f.at)}>{relativeTime(f.at, now)}</span> },
          { key: 'ip', header: 'IP', mono: true, render: (f) => f.ip || '—' },
          { key: 'client', header: '客户端', render: (f) => <span title={f.ua}>{f.family || '未知'}{f.format ? ` · ${f.format}` : ''}</span> },
          { key: 'result', header: '结果', width: '72px', render: (f) => <Tag tone={f.result === 'ok' ? 'ok' : 'warn'}>{f.result}</Tag> },
        ]}
        rows={p.fetches}
        rowKey={(f) => f.at + f.ip + f.ua}
        empty={<Empty bare title="没有拉取记录" description="客户端导入订阅后会出现在这里。" />}
      />

      <h3 className={css.sectionTitle}>最近安全事件</h3>
      <Table
        label="安全事件"
        columns={[
          { key: 'at', header: '时间', width: '84px', render: (e) => <span title={formatDateTime(e.at)}>{relativeTime(e.at, now)}</span> },
          { key: 'action', header: '事件', mono: true, render: (e) => e.action },
          { key: 'outcome', header: '结果', width: '72px', render: (e) => <Tag tone={e.outcome === 'success' ? 'ok' : 'warn'}>{e.outcome}</Tag> },
          { key: 'ip', header: 'IP', mono: true, render: (e) => e.ip || '—' },
        ]}
        rows={p.events.slice(0, 20)}
        rowKey={(e) => e.at + e.action + e.ip}
        empty={<Empty bare title="没有安全事件" description="登录、改密等动作会记在这里。" />}
      />
    </>
  )
}
