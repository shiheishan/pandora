import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, Checkbox, Input, QueryView, Switch, useToast } from '../../../ui'
import { acmeBody, acmeFormFrom, type AcmeForm } from './logic'
import { useAcmeSettings, useCan, useFailure, useInvalidateCerts } from './queries'
import { acmeSettingsSchema, type AcmeSettings } from './schemas'
import css from './certs.module.css'

export function AcmeTab() {
  const settings = useAcmeSettings()
  return (
    <QueryView query={settings} rows={4} empty={null}>
      {(saved) => <AcmeFormView saved={saved} />}
    </QueryView>
  )
}

function AcmeFormView({ saved }: { saved: AcmeSettings }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateCerts()
  const [draft, setDraft] = useState<AcmeForm | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const form = draft ?? acmeFormFrom(saved)
  const dirty = draft !== null && JSON.stringify(draft) !== JSON.stringify(acmeFormFrom(saved))
  const writable = can('node.certificate.write')
  const edit = (patch: Partial<AcmeForm>) => setDraft({ ...form, ...patch })

  const save = useMutation({
    mutationFn: (body: object) => api.put('v1/settings/acme', acmeSettingsSchema, { body }),
    onSuccess: () => {
      toast('ACME 设置已保存')
      void invalidate().then(() => setDraft(null))
    },
    onError: (e) => fail(e, setErrors),
  })
  const submit = () => {
    const out = acmeBody(form, saved)
    if ('errors' in out) return setErrors(out.errors)
    setErrors({})
    save.mutate(out.body)
  }
  const disabled = !writable || save.isPending

  return (
    <div className={css.stack}>
      {saved.directory_override && <div className={css.notice}>这台面板的签发被测试环境的 ACME 目录覆盖接管，下面的 CA 选择不生效。</div>}
      <Card title="签发用的 CA">
        <div className={css.form}>
          <Input
            label="联系邮箱"
            fieldClassName={css.span2}
            value={form.contactEmail}
            placeholder="可以留空"
            hint="注册 ACME 账号时交给 CA；Let's Encrypt 不再发到期提醒邮件，可以不填。保存设置即表示同意所用 CA 的服务条款。"
            error={errors.contact_email}
            disabled={disabled}
            onChange={(e) => edit({ contactEmail: e.target.value })}
          />
          <div className={css.span2}>
            <Switch label="使用 Let's Encrypt 测试环境（staging）" checked={form.useStaging} disabled={disabled} onChange={(e) => edit({ useStaging: e.target.checked })} />
            <div className={css.faint}>测试环境签的证书客户端不信任，只用来试流程、避开正式环境的限额。</div>
          </div>
        </div>
      </Card>
      <Card title="备用 CA：ZeroSSL（默认关）">
        <div className={css.form}>
          <div className={css.span2}>
            <Switch label="Let's Encrypt 预计超出每周限额时改用 ZeroSSL" checked={form.zerosslEnabled} disabled={disabled} onChange={(e) => edit({ zerosslEnabled: e.target.checked })} />
            <div className={css.faint}>只在证书快到期（剩不到 7 天）或从没签出过时切换。需要先在 ZeroSSL 后台的 Developer 页生成 EAB 凭据。</div>
          </div>
          <Input label="EAB KID" mono value={form.eabKid} error={errors.zerossl_eab_kid} disabled={disabled} onChange={(e) => edit({ eabKid: e.target.value })} />
          <Input
            label="EAB HMAC Key"
            type="password"
            autoComplete="off"
            mono
            value={form.eabHmac}
            placeholder={saved.zerossl_eab_hmac_set ? '已保存，留空不修改' : '未设置'}
            error={errors.zerossl_eab_hmac}
            disabled={disabled || form.clearHmac}
            onChange={(e) => edit({ eabHmac: e.target.value })}
          />
          {saved.zerossl_eab_hmac_set && (
            <div className={css.span2}>
              <Checkbox label="清除已保存的 EAB HMAC" checked={form.clearHmac} disabled={disabled} onChange={(e) => edit({ clearHmac: e.target.checked, eabHmac: '' })} />
            </div>
          )}
        </div>
      </Card>
      {writable && (
        <div className={css.toolbar}>
          {dirty && <span className={css.faint}>有未保存的修改</span>}
          <span className={css.spacer} />
          <Button variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
            保存
          </Button>
        </div>
      )}
    </div>
  )
}
