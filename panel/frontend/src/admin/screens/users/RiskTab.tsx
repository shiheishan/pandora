import { href } from '../../../core/router'
import { QueryView, Tag } from '../../../ui'
import { useUserProfile, type UserDetail } from './api'
import css from './Detail.module.css'
import { LOGIN_IPS_WARN, loginIps7d, RISK_VIEW } from './model'

export function RiskTab({ d, now }: { d: UserDetail; now: Date }) {
  const q = useUserProfile(d.id)
  const risk = RISK_VIEW[d.risk_level]
  return (
    <QueryView query={q} rows={4} isEmpty={() => false} empty={null}>
      {(p) => {
        const ipCount = loginIps7d(p.ips, now)
        const shared = p.related.length > 0
        return (
          <>
            <section className={css.section} aria-labelledby="risk-assess">
              <h3 id="risk-assess" className={css.sectionTitle}>评估</h3>
              <dl className={css.facts}>
                <div className={css.factRow}>
                  <dt>风险等级</dt>
                  <dd>
                    <span className={css.factValue}>
                      <Tag tone={risk.tone}>{risk.label}</Tag>
                    </span>
                  </dd>
                </div>
              </dl>
            </section>

            <section className={css.section} aria-labelledby="risk-network">
              <h3 id="risk-network" className={css.sectionTitle}>网络与登录</h3>
              <dl className={css.facts}>
                <div className={css.factRow}>
                  <dt>注册 IP</dt>
                  <dd>
                    <span className={css.factValue}>
                      <span className={css.mono}>{p.registered_ip || '无记录'}</span>
                    </span>
                  </dd>
                </div>
                <div className={css.factRow}>
                  <dt>近 7 天登录 IP</dt>
                  <dd>
                    <span className={css.factValue}>
                      <span className={ipCount >= LOGIN_IPS_WARN ? css.tone_warn : undefined}>{ipCount} 个</span>
                    </span>
                  </dd>
                </div>
                <div className={css.factRow}>
                  <dt>共享 IP 聚类</dt>
                  <dd>
                    {shared ? (
                      <div className={css.factStack}>
                        <span className={css.tone_danger}>与 {p.related.length} 个账号共用</span>
                        <ul className={css.related}>
                          {p.related.map((r) => (
                            <li key={r.id}>
                              <a className={css.link} href={href(`/users/list/${encodeURIComponent(r.id)}/risk`)}>
                                {r.email}
                              </a>
                            </li>
                          ))}
                        </ul>
                      </div>
                    ) : (
                      <span className={css.factValue}>无</span>
                    )}
                  </dd>
                </div>
              </dl>
            </section>
          </>
        )
      }}
    </QueryView>
  )
}
