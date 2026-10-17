import { useMutation, useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Input, QueryView, Select, Table, useToast } from '../../../ui'
import {
  addDomainBody,
  defaultScopeValue,
  domainConfigSchema,
  domainScopeOptions,
  flattenDomains,
  hostOf,
  removeDomainBody,
  scopeForGroup,
  userGroupOptionsSchema,
  type DomainConfig,
  type DomainGroupBody,
  type UserGroupOptions,
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
  const userGroups = useQuery({
    queryKey: ['admin', 'nodes', 'subscription', 'user-groups'],
    queryFn: ({ signal }) => api.get('v1/user-groups', userGroupOptionsSchema, { signal }).then((r) => r.groups),
  })
  return (
    <QueryView query={domains} rows={4} empty={null}>
      {(saved) => <DomainsFlatView saved={saved} userGroups={userGroups.data ?? []} />}
    </QueryView>
  )
}

interface PendingRemove {
  groupId: string
  baseUrl: string
  host: string
  groupName: string
  scope: string
  /** 被删的是缺省组：没分到组的用户改用面板对外地址 */
  isDefault: boolean
}

function DomainsFlatView({ saved, userGroups }: { saved: DomainConfig; userGroups: UserGroupOptions['groups'] }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSubscription()
  const [input, setInput] = useState('')
  const [inputError, setInputError] = useState<string | undefined>()
  const [alert, setAlert] = useState<string | undefined>()
  const [pendingRemove, setPendingRemove] = useState<PendingRemove | null>(null)
  const [pickedScope, setScope] = useState(() => defaultScopeValue(saved))

  const ugForScope = useMemo(() => userGroups.map((g) => ({ id: g.id, name: g.name })), [userGroups])
  const scopeOptions = useMemo(() => domainScopeOptions(saved, ugForScope), [saved, ugForScope])
  const rows = useMemo(() => flattenDomains(saved, ugForScope), [saved, ugForScope])

  // 保存后配置刷新：当前选的作用范围已不在选项里（如组刚被整组删掉）时退回缺省值
  const scope = scopeOptions.some((o) => o.value === pickedScope) ? pickedScope : defaultScopeValue(saved)

  const save = useMutation({
    mutationFn: (body: { groups: DomainGroupBody[] }) => api.put('v1/subscription-domains', domainConfigSchema, { body }),
    onSuccess: () => void invalidate(),
  })

  const disabled = save.isPending

  const onSaveError = (e: unknown) => {
    if (isApiError(e) && (e.status === 422 || e.status === 409)) {
      const msg = Object.values(e.fields)[0] ?? e.message
      setAlert(msg)
      return
    }
    fail(e)
  }

  const add = () => {
    const out = addDomainBody(saved, input, scope)
    if ('error' in out) {
      setInputError(out.error)
      return
    }
    setInputError(undefined)
    setAlert(undefined)
    save.mutate(out.body, {
      onSuccess: () => {
        toast(`已添加 ${out.host}，最多 30 秒后在新生成的链接里生效`)
        setInput('')
      },
      onError: onSaveError,
    })
  }

  const remove = (groupId: string, baseUrl: string) => {
    const { body, dropsGroup, groupName } = removeDomainBody(saved, groupId, baseUrl)
    const host = hostOf(baseUrl)
    if (dropsGroup) {
      const g = saved.groups.find((x) => x.id === groupId)
      const scopeText = g ? scopeForGroup(g, saved.groups, ugForScope) : ''
      setPendingRemove({ groupId, baseUrl, host, groupName, scope: scopeText, isDefault: g?.is_default ?? false })
      return
    }
    setAlert(undefined)
    save.mutate(body, {
      onSuccess: () => toast(`已移除 ${host}`),
      onError: onSaveError,
    })
  }

  const confirmRemove = async () => {
    if (!pendingRemove) return
    const { host } = pendingRemove
    const { body } = removeDomainBody(saved, pendingRemove.groupId, pendingRemove.baseUrl)
    setAlert(undefined)
    try {
      await save.mutateAsync(body)
      toast(`已移除 ${host}`)
    } catch (e) {
      onSaveError(e)
    } finally {
      // 出错也关掉确认框，错误显示在添加行下方
      setPendingRemove(null)
    }
  }

  return (
    <Card title="订阅域名" subtitle="用户的订阅链接用这些域名" flush>
      <div className={css.addRow}>
        <Input
          aria-label="新域名"
          mono
          size="sm"
          placeholder="sub.example.com"
          value={input}
          disabled={disabled}
          error={inputError}
          fieldClassName={css.addInput}
          onChange={(e) => {
            setInput(e.target.value)
            if (inputError) setInputError(undefined)
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
        />
        <Select
          aria-label="作用范围"
          size="sm"
          value={scope}
          onChange={(e) => setScope(e.target.value)}
          options={scopeOptions}
        />
        <Button size="sm" disabled={disabled} busy={save.isPending} onClick={add}>
          添加
        </Button>
      </div>
      {alert && (
        <div className={css.alertWrap}>
          <div className={nodesCss.errorBox} role="alert">
            {alert}
          </div>
        </div>
      )}
      <Table
        label="订阅域名"
        rows={rows}
        rowKey={(r) => `${r.groupId}:${r.baseUrl}`}
        empty="还没有订阅域名，订阅链接用面板对外地址。"
        columns={[
          { key: 'host', header: '域名', mono: true, render: (row) => row.host },
          {
            key: 'scope',
            header: '作用范围',
            render: (row) => (
              <span>
                {row.groupName}
                <span className={css.scopeHint}> · {row.scope}</span>
              </span>
            ),
          },
          {
            key: 'actions',
            header: '',
            align: 'right',
            render: (row) => (
              <Button size="sm" variant="link" disabled={disabled} onClick={() => remove(row.groupId, row.baseUrl)}>
                移除
              </Button>
            ),
          },
        ]}
      />
      <ConfirmModal
        open={pendingRemove !== null}
        title={pendingRemove ? `移除 ${pendingRemove.host}？` : ''}
        body={
          pendingRemove
            ? pendingRemove.isDefault
              ? `这是缺省组「${pendingRemove.groupName}」里最后一个域名，移除后这个组一起删掉，作用范围「${pendingRemove.scope}」的用户改用面板对外地址。`
              : `这是「${pendingRemove.groupName}」组里最后一个域名，移除后这个组一起删掉，作用范围「${pendingRemove.scope}」的用户改用缺省组的域名；没有缺省组时用面板对外地址。`
            : null
        }
        confirmLabel="移除"
        tone="danger"
        onConfirm={confirmRemove}
        onCancel={() => setPendingRemove(null)}
      />
    </Card>
  )
}
