import { useState } from 'react'
import { formatDateTime, relativeTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { Button, Empty, QueryView, Table, Tag } from '../../../ui'
import { Callout, KvList, KvRow, SectionTitle } from '../../kit'
import { TERM_HINT } from '../security/logic'
import { networkLabel } from '../security/trace'
import { useUserProfile, type UserDetail } from './api'
import css from './Detail.module.css'
import { recentIps } from './detail'
import { LOGIN_IPS_WARN, loginIps7d, PROFILE_RISK_VIEW } from './model'

export function RiskTab({ d, now }: { d: UserDetail; now: Date }) {
  const q = useUserProfile(d.id)
  const [showFetchIp, setShowFetchIp] = useState(false)
  return (
    <QueryView query={q} rows={4} isEmpty={() => false} empty={null}>
      {(p) => {
        const risk = PROFILE_RISK_VIEW[p.risk]
        const ipCount = loginIps7d(p.ips, now)
        const ipWarn = ipCount >= LOGIN_IPS_WARN
        const shared = p.related.length > 0
        return (
          <>
            <section>
              <SectionTitle
                action={
                  <a className={css.link} href={href('/security/audit', { q: d.id })}>
                    看审计记录 →
                  </a>
                }
              >
                评估
              </SectionTitle>
              <KvList boxed labelWidth={140}>
                <KvRow label="风险等级">
                  <Tag tone={risk.tone} title={TERM_HINT.clusterRisk}>
                    {risk.label}
                  </Tag>{' '}
                  <span className={css.muted}>按共用 IP 推出，与风控同一规则</span>
                </KvRow>
                <KvRow label="注册 IP">
                  <span className={css.mono}>{p.registered_ip || '无记录'}</span>
                </KvRow>
                <KvRow label="近 7 天用过的 IP">
                  <span className={ipWarn ? css.warn : undefined}>{ipCount} 个</span>
                </KvRow>
                <KvRow label="共享 IP 聚类">
                  {shared ? (
                    <span className={css.sharedWrap}>
                      <span className={css.danger}>与 {p.related.length} 个账号共用</span>
                      <span className={css.sharedList}>
                        {p.related.map((r) => (
                          <a key={r.id} className={css.link} href={href(`/users/list/${encodeURIComponent(r.id)}/risk`)}>
                            {r.email}
                          </a>
                        ))}
                      </span>
                      <span className={css.muted}>
                        近 90 天里这些账号和他用过同一个出口 IP，可能是一家人、同一公司，也可能是共享或批量注册；在「安全与运维 › 风控」统一处置。
                      </span>
                    </span>
                  ) : (
                    <>
                      无<span className={css.muted}>（近 90 天用过的 IP 都没有别的账号用过）</span>
                    </>
                  )}
                </KvRow>
              </KvList>
            </section>

            <section>
              <SectionTitle
                hint="登录、改密码、下单等本人操作 · 按最近一次出现排序"
                action={
                  <a className={css.link} href={href('/security/access', { view: 'login', user: d.id })}>
                    看登录记录 →
                  </a>
                }
              >
                最近使用的 IP
              </SectionTitle>
              <Table
                boxed
                label="最近使用的 IP"
                rows={recentIps(p.ips)}
                rowKey={(r) => r.ip}
                empty={<Empty bare title="还没有登录记录" description="用户登录后，这里会列出出口 IP。" />}
                columns={[
                  { key: 'ip', header: 'IP', mono: true, render: (r) => r.ip },
                  {
                    key: 'geo',
                    header: '归属地',
                    hideOnMobile: true,
                    render: (r) => [r.geo, networkLabel(r.network_kind)].filter(Boolean).join(' · ') || '—',
                  },
                  { key: 'count', hideOnMobile: true, header: '次数', align: 'right', render: (r) => r.count },
                  { key: 'first', hideOnMobile: true, header: '首次', render: (r) => <span className={css.muted}>{formatDateTime(r.first)}</span> },
                  { key: 'last', header: '最近', align: 'right', render: (r) => <span className={css.muted}>{relativeTime(r.last, now)}</span> },
                  {
                    key: 'accounts',
                    hideOnMobile: true,
                    header: '同 IP 账号',
                    align: 'right',
                    render: (r) =>
                      r.risk ? (
                        <>
                          {r.accounts} <Tag tone={PROFILE_RISK_VIEW[r.risk].tone}>{PROFILE_RISK_VIEW[r.risk].label}</Tag>
                        </>
                      ) : (
                        r.accounts
                      ),
                  },
                ]}
              />
            </section>

            <section>
              <SectionTitle
                hint={`近 7 天 ${p.fetch_sources_7d} 个来源 · 最近 ${p.fetches.length} 次`}
                action={
                  <Button size="sm" variant="link" onClick={() => setShowFetchIp((v) => !v)}>
                    {showFetchIp ? '隐藏' : '显示完整 IP'}
                  </Button>
                }
              >
                最近订阅拉取
              </SectionTitle>
              <Table
                boxed
                label="最近订阅拉取"
                rows={p.fetches}
                rowKey={(r) => `${r.at}-${r.ip}-${r.ua}`}
                empty={<Empty bare title="还没有订阅拉取" description="用户在客户端更新订阅后，这里会列出来源与客户端。" />}
                columns={[
                  {
                    key: 'ip',
                    header: 'IP',
                    mono: true,
                    render: (r) => {
                      if (!r.ip) return '—'
                      const shown = showFetchIp ? r.ip : maskIp(r.ip)
                      return shown || '—'
                    },
                  },
                  {
                    key: 'family',
                    header: '客户端',
                    hideOnMobile: true,
                    render: (r) => <span title={r.ua}>{r.family}</span>,
                  },
                  { key: 'result', header: '结果', render: (r) => fetchResultLabel(r.result) },
                  {
                    key: 'at',
                    header: '时间',
                    align: 'right',
                    render: (r) => <span className={css.muted}>{relativeTime(r.at, now)}</span>,
                  },
                ]}
              />
            </section>

            {!shared && (
              <Callout action={<Button size="sm" variant="link" onClick={() => navigate('/security/risk')}>去风控</Button>}>
                这个用户的出口 IP 没有和其他账号共用。共用情况在「安全与运维 › 风控」里按 IP 汇总。
              </Callout>
            )}
          </>
        )
      }}
    </QueryView>
  )
}

/** 与 identity/service/trace.go 的 maskIP 同口径 */
function maskIp(ip: string): string {
  let s = ip.trim()
  if (!s) return ''
  if (s.toLowerCase().startsWith('::ffff:')) s = s.slice(7)
  if (s.includes('.') && /^\d+\.\d+\.\d+\.\d+$/.test(s)) {
    const [a, b] = s.split('.')
    return `${a}.${b}.*.*`
  }
  if (s.includes(':')) {
    const parts = s.split(':')
    if (parts.length >= 2) return `${parts[0]}:${parts[1]}:*`
  }
  return '*'
}

function fetchResultLabel(result: string): string {
  switch (result) {
    case 'ok':
      return '成功'
    case 'expired':
      return '已过期提示'
    case 'rate_limited':
      return '被限流'
    case 'error':
      return '失败'
    case 'not_found':
      return '链接无效'
    default:
      return result
  }
}
