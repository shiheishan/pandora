import { formatDateTime, relativeTime } from '../../../core/format'
import { href, navigate } from '../../../core/router'
import { Button, Empty, QueryView, Table, Tag } from '../../../ui'
import { Callout, KvList, KvRow, SectionTitle } from '../../kit'
import { useUserProfile, type UserDetail } from './api'
import css from './Detail.module.css'
import { recentIps } from './detail'
import { LOGIN_IPS_WARN, loginIps7d, RISK_VIEW } from './model'

export function RiskTab({ d, now }: { d: UserDetail; now: Date }) {
  const q = useUserProfile(d.id)
  const risk = RISK_VIEW[d.risk_level]
  return (
    <QueryView query={q} rows={4} isEmpty={() => false} empty={null}>
      {(p) => {
        const ipCount = loginIps7d(p.ips, now)
        const ipWarn = ipCount >= LOGIN_IPS_WARN
        const shared = p.related.length > 0
        return (
          <>
            <section>
              <SectionTitle>评估</SectionTitle>
              <KvList boxed labelWidth={140}>
                <KvRow label="风险等级">
                  <Tag tone={risk.tone}>{risk.label}</Tag>
                </KvRow>
                <KvRow label="注册 IP">
                  <span className={css.mono}>{p.registered_ip || '无记录'}</span>
                </KvRow>
                <KvRow label="近 7 天登录 IP">
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
                    </span>
                  ) : (
                    '无'
                  )}
                </KvRow>
              </KvList>
            </section>

            <section>
              <SectionTitle hint="按最近一次出现排序">最近登录 IP</SectionTitle>
              <Table
                boxed
                label="最近登录 IP"
                rows={recentIps(p.ips)}
                rowKey={(r) => r.ip}
                empty={<Empty bare title="还没有登录记录" description="用户登录后，这里会列出出口 IP。" />}
                columns={[
                  { key: 'ip', header: 'IP', mono: true, render: (r) => r.ip },
                  { key: 'count', header: '登录次数', align: 'right', render: (r) => r.count },
                  { key: 'first', header: '首次', render: (r) => <span className={css.muted}>{formatDateTime(r.first)}</span> },
                  { key: 'last', header: '最近', align: 'right', render: (r) => <span className={css.muted}>{relativeTime(r.last, now)}</span> },
                  { key: 'accounts', header: '同 IP 账号', align: 'right', render: (r) => r.accounts },
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
