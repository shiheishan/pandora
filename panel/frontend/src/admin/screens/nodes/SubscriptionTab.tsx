import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, QueryView, Switch, useToast } from '../../../ui'
import { extrasBody, extrasFormFrom, subscriptionExtrasSchema, type ExtrasForm, type SubscriptionExtras } from './subscription'
import { useFailure, useInvalidateSubscription, useSubscriptionExtras } from './subscriptionQueries'
import { SubscriptionDomains } from './SubscriptionDomains'
import { SubscriptionTemplates } from './SubscriptionTemplates'
import css from './nodes.module.css'

export function SubscriptionTab() {
  return (
    <div className={css.stack}>
      <ExtrasCard />
      <SubscriptionDomains />
      <SubscriptionTemplates />
    </div>
  )
}

function ExtrasCard() {
  const settings = useSubscriptionExtras()
  return (
    <QueryView query={settings} rows={4} empty={null}>
      {(saved) => <ExtrasFormView saved={saved} />}
    </QueryView>
  )
}

function ExtrasFormView({ saved }: { saved: SubscriptionExtras }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSubscription()
  const [draft, setDraft] = useState<ExtrasForm | null>(null)
  const form = draft ?? extrasFormFrom(saved)
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(extrasFormFrom(saved))
  const edit = (patch: Partial<ExtrasForm>) => setDraft({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/settings/subscription', subscriptionExtrasSchema, { body }),
    onSuccess: () => {
      toast('订阅附加设置已保存，最多 30 秒后在订阅里生效')
      void invalidate().then(() => setDraft(null))
    },
    // 只剩两个开关，没有可标红的字段：服务端校验失败一律弹提示
    onError: (e) => fail(e),
  })

  const submit = () => {
    const { body } = extrasBody(form, saved)
    if (Object.keys(body).length === 0) return
    save.mutate(body)
  }

  const disabled = save.isPending

  return (
    <>
      <Card title="订阅附加设置">
        <div className={css.form}>
          <div className={css.span2}>
            <Switch
              label="订阅里显示剩余流量、距下次重置、到期（信息节点）"
              checked={form.infoNodes}
              disabled={disabled}
              onChange={(e) => edit({ infoNodes: e.target.checked })}
            />
            <div className={css.faint}>
              开启后订阅最前面多出几条名字是流量与到期信息的节点，地址是本机占位、连不上；只为在客户端里看到这些信息。
            </div>
          </div>
          <div className={css.span2}>
            <Switch
              label="节点名前加协议前缀"
              checked={form.protocolPrefix}
              disabled={disabled}
              onChange={(e) => edit({ protocolPrefix: e.target.checked })}
            />
            <div className={css.faint}>如 [vless] 香港 01、[Hy2] 东京 02，方便在客户端里分辨协议。</div>
          </div>
        </div>
      </Card>
      <div className={css.toolbar}>
        {dirty && <span className={css.faint}>有未保存的修改</span>}
        <span className={css.spacer} />
        <Button variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
          保存
        </Button>
      </div>
    </>
  )
}
