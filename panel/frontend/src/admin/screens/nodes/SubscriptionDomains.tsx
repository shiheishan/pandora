import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, Input, QueryView, Switch, useToast } from '../../../ui'
import {
  bodyFrom,
  domainConfigSchema,
  effectiveText,
  formFrom,
  newGroup,
  userGroupOptionsSchema,
  type DomainConfig,
  type DomainGroupBody,
  type DomainRowForm,
  type GroupForm,
} from './domainGroups'
import { SUB_KEY, useFailure, useInvalidateSubscription } from './subscriptionQueries'
import nodesCss from './nodes.module.css'
import css from './SubscriptionDomains.module.css'

export function SubscriptionDomains() {
  const api = useApi()
  const domains = useQuery({
    queryKey: [...SUB_KEY, 'domains'],
    queryFn: ({ signal }) => api.get('v1/subscription-domains', domainConfigSchema, { signal }),
  })
  // 只取用户组的 id 与 name
  const userGroups = useQuery({
    queryKey: ['admin', 'nodes', 'subscription', 'user-groups'],
    queryFn: ({ signal }) => api.get('v1/user-groups', userGroupOptionsSchema, { signal }).then((r) => r.groups),
  })
  return (
    <QueryView query={domains} rows={4} empty={null}>
      {(saved) => <DomainsFormView saved={saved} userGroups={userGroups.data ?? []} />}
    </QueryView>
  )
}

function DomainsFormView({ saved, userGroups }: { saved: DomainConfig; userGroups: Array<{ id: string; name: string }> }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSubscription()
  const [draft, setDraft] = useState<GroupForm[] | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const limits = saved.limits
  const base = formFrom(saved)
  const form = draft ?? base
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(base)

  const setForm = (next: GroupForm[]) => setDraft(next)

  const editGroup = (key: string, patch: Partial<GroupForm>) =>
    setForm(form.map((g) => (g.key === key ? { ...g, ...patch } : g)))

  const editRow = (key: string, idx: number, patch: Partial<DomainRowForm>) => {
    const g = form.find((x) => x.key === key)!
    editGroup(key, { domains: g.domains.map((d, i) => (i === idx ? { ...d, ...patch } : d)) })
  }

  /** 打开一个组的缺省时自动关掉其他组的 */
  const setDefault = (key: string, on: boolean) =>
    setForm(form.map((g) => ({ ...g, isDefault: g.key === key ? on : on ? false : g.isDefault })))

  const toggleUserGroup = (key: string, id: string, on: boolean) => {
    const g = form.find((x) => x.key === key)!
    editGroup(key, { userGroupIds: on ? [...g.userGroupIds, id] : g.userGroupIds.filter((x) => x !== id) })
  }

  const addDomain = (key: string) => {
    const g = form.find((x) => x.key === key)!
    editGroup(key, { domains: [...g.domains, { baseUrl: '', weight: '1' }] })
  }

  const removeDomain = (key: string, idx: number) => {
    const g = form.find((x) => x.key === key)!
    editGroup(key, { domains: g.domains.filter((_, i) => i !== idx) })
  }

  const removeGroup = (key: string) => setForm(form.filter((g) => g.key !== key))

  const addGroup = () => setForm([...form, newGroup(form)])

  const save = useMutation({
    mutationFn: (body: { groups: DomainGroupBody[] }) => api.put('v1/subscription-domains', domainConfigSchema, { body }),
    onSuccess: () => {
      toast('订阅域名已保存，最多 30 秒后在新生成的链接里生效')
      void invalidate().then(() => setDraft(null))
    },
    onError: (e) => fail(e, setErrors),
  })

  const submit = () => {
    const out = bodyFrom(form, limits)
    if ('error' in out) return setErrors({ groups: out.error })
    setErrors({})
    save.mutate(out.body)
  }

  const disabled = save.isPending

  /** 已保存的同地址域名（新加或改过地址的行找不到，调用方显示「保存后开始统计」） */
  const savedDomain = (key: string, baseUrl: string) =>
    saved.groups.find((g) => g.id === key)?.domains.find((d) => d.base_url === baseUrl.trim())

  return (
    <>
      <Card title="订阅域名">
        <div className={css.cardBody}>
          <p className={nodesCss.faint}>
            用户拿到的订阅链接用哪个域名：按用户组分到各组，没分到的用缺省组；组内按有效权重随机取（有效权重 =
            基础权重 × 分数，分数随拉取与门户探测的成败自动升降，在 0.1–3 之间）。一个组都不建就用面板对外地址。域名要先解析到本机、在部署配置
            PANDORA_SUB_DOMAINS 里登记并放好证书（见 RUNBOOK 第 14 章），否则链接打不开。
          </p>
          {!saved.scores_available && (
            <p className={nodesCss.faint}>暂时读不到分数（Redis 不可用），现在按基础权重分配。</p>
          )}
          {errors.groups && (
            <div className={nodesCss.errorBox} role="alert">
              {errors.groups}
            </div>
          )}
          <div className={css.groups}>
            {form.map((g, gi) => (
              <div key={g.key} className={css.group}>
                <div className={css.groupHead}>
                  <div>
                    <div className={css.groupNo}>第 {gi + 1} 组</div>
                    <Input
                      label="组名"
                      value={g.name}
                      disabled={disabled}
                      onChange={(e) => editGroup(g.key, { name: e.target.value })}
                    />
                  </div>
                  <Switch
                    label="缺省组（没分到组的用户都用它）"
                    checked={g.isDefault}
                    disabled={disabled}
                    onChange={(e) => setDefault(g.key, e.target.checked)}
                  />
                </div>
                <div className={css.userGroups}>
                  <span className={css.subLabel}>分给用户组</span>
                  {userGroups.length === 0 ? (
                    <span className={nodesCss.faint}>还没有用户组</span>
                  ) : (
                    userGroups.map((ug) => {
                      const owner = form.find((x) => x.userGroupIds.includes(ug.id))
                      const taken = owner != null && owner.key !== g.key
                      return (
                        <Checkbox
                          key={ug.id}
                          label={taken ? `${ug.name}（已在「${owner.name.trim() || '未命名'}」）` : ug.name}
                          checked={g.userGroupIds.includes(ug.id)}
                          disabled={disabled || taken}
                          onChange={(e) => toggleUserGroup(g.key, ug.id, e.target.checked)}
                        />
                      )
                    })
                  )}
                </div>
                <div className={css.domainRows}>
                  <span className={css.subLabel}>域名（组内按权重随机取）</span>
                  {g.domains.map((row, ri) => {
                    const d = savedDomain(g.key, row.baseUrl)
                    return (
                      <div key={ri} className={css.domainRow}>
                        <Input
                          aria-label={`第 ${ri + 1} 个域名地址`}
                          placeholder="https://sub1.example.com"
                          value={row.baseUrl}
                          disabled={disabled}
                          onChange={(e) => editRow(g.key, ri, { baseUrl: e.target.value })}
                        />
                        <Input
                          aria-label={`第 ${ri + 1} 个域名权重`}
                          inputMode="numeric"
                          value={row.weight}
                          disabled={disabled}
                          onChange={(e) => editRow(g.key, ri, { weight: e.target.value })}
                        />
                        <span className={css.effective} title={d ? undefined : '保存后开始统计'}>
                          {d ? effectiveText(d, saved.scores_available) : '保存后开始统计'}
                        </span>
                        <Button size="sm" disabled={disabled} onClick={() => removeDomain(g.key, ri)}>
                          删除
                        </Button>
                      </div>
                    )
                  })}
                  <div className={css.domainAdd}>
                    <Button
                      size="sm"
                      disabled={disabled || g.domains.length >= limits.domains_per_group}
                      onClick={() => addDomain(g.key)}
                    >
                      加一个域名
                    </Button>
                  </div>
                </div>
                <div className={css.groupFooter}>
                  <Button size="sm" disabled={disabled} onClick={() => removeGroup(g.key)}>
                    删除这个组
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
      </Card>
      <div className={nodesCss.toolbar}>
        <Button size="sm" disabled={disabled || form.length >= limits.groups} onClick={addGroup}>
          新建一个组
        </Button>
        {dirty && <span className={nodesCss.faint}>有未保存的修改</span>}
        <span className={nodesCss.spacer} />
        <Button variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      </div>
    </>
  )
}
