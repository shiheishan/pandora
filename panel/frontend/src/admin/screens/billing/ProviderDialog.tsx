import { useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, Input, Modal, Select, useToast } from '../../../ui'
import { useFailure, useIntentKey } from '../../actions'
import { providerWrittenSchema, useInvalidateBilling, type Provider } from './api'
import css from './Billing.module.css'
import { EPAY_METHODS, emptyProviderForm, methodLabel, providerBody, providerFormFrom, providerProblems, toggleMethod, type ProviderForm } from './model'

/** 新建（editing 为空）或编辑一个易支付渠道。每次打开都是一张新表单，也是一次新的写意图 */
export function ProviderDialog({ open, editing, onClose }: { open: boolean; editing: Provider | null; onClose: () => void }) {
  return open ? <ProviderFormModal key={editing?.id ?? 'new'} editing={editing} onClose={onClose} /> : null
}

function ProviderFormModal({ editing, onClose }: { editing: Provider | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateBilling()
  const mode = editing ? 'edit' : 'create'
  const [form, setForm] = useState<ProviderForm>(() => (editing ? providerFormFrom(editing) : emptyProviderForm()))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const set = <K extends keyof ProviderForm>(key: K, value: ProviderForm[K]) => {
    setForm((f) => ({ ...f, [key]: value }))
    setErrors({})
  }

  const submit = async () => {
    const problems = providerProblems(form, mode, editing?.has_credentials ?? false)
    if (Object.keys(problems).length) return setErrors(problems)
    const body = providerBody(form, mode)
    setBusy(true)
    try {
      if (editing) {
        const r = await api.put(`v1/payment-providers/${encodeURIComponent(editing.code)}`, providerWrittenSchema, {
          body,
          idempotencyKey: intent.keyFor([editing.code, body]),
        })
        toast(r.credentials_changed ? `${form.display_name.trim()} 已保存，凭据已更新` : `${form.display_name.trim()} 已保存`)
      } else {
        const r = await api.post('v1/payment-providers', providerWrittenSchema, { body, idempotencyKey: intent.keyFor(body) })
        toast(`渠道 ${r.code} 已创建，核对无误后在卡片上打开「收新单」`)
      }
      intent.reset()
      onClose()
    } catch (e) {
      fail(e, { fields: setErrors, intent })
    } finally {
      setBusy(false)
      void invalidate()
    }
  }

  const keepHint = editing?.has_credentials ? '已配置，不回显；留空表示不改' : undefined
  return (
    <Modal
      open
      size="md"
      onClose={onClose}
      dismissible={!busy}
      title={editing ? `编辑 ${editing.display_name}` : '新建易支付渠道'}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={busy}>
            取消
          </Button>
          <Button size="dialog" variant="primary" busy={busy} onClick={() => void submit()}>
            {editing ? '保存' : '创建'}
          </Button>
        </>
      }
    >
      <div className={css.dialogBody}>
        <div className={css.grid2}>
          <Input
            label="渠道编码"
            mono
            placeholder="如 epay2"
            hint={editing ? '建后不可改' : '回调地址里用它，建后不可改'}
            value={form.code}
            onChange={(e) => set('code', e.target.value)}
            error={errors.code}
            disabled={editing !== null}
            data-autofocus={editing ? undefined : ''}
          />
          <Input label="名称" value={form.display_name} onChange={(e) => set('display_name', e.target.value)} error={errors.display_name} />
        </div>
        <Input label="站点地址" mono placeholder="https://pay.example.com" value={form.base_url} onChange={(e) => set('base_url', e.target.value)} error={errors.base_url} />
        <div className={css.grid2}>
          <Input label="下单路径" mono value={form.submit_path} onChange={(e) => set('submit_path', e.target.value)} error={errors.submit_path} />
          <Input label="查询接口路径" mono value={form.api_path} onChange={(e) => set('api_path', e.target.value)} error={errors.api_path} />
        </div>
        <fieldset className={`${css.stack} ${css.fieldset}`} aria-describedby={errors.methods ? 'provider-methods-error' : undefined}>
          <legend className={css.fieldLabel}>支付方式（结账页按勾选顺序列出）</legend>
          <div className={css.toolbar}>
            {EPAY_METHODS.map((m) => (
              <Checkbox
                key={m.value}
                label={m.label}
                checked={form.methods.includes(m.value)}
                onChange={(e) => {
                  setForm((f) => toggleMethod(f, m.value, e.target.checked))
                  setErrors({})
                }}
              />
            ))}
          </div>
          {errors.methods && (
            <span id="provider-methods-error" role="alert" className={`${css.small} ${css.tone_danger}`}>
              {errors.methods}
            </span>
          )}
        </fieldset>
        <Select
          label="默认方式"
          hint="用户没选方式时用它"
          options={form.methods.map((m) => ({ value: m, label: methodLabel(m) }))}
          value={form.default_method}
          onChange={(e) => set('default_method', e.target.value)}
          error={errors.default_method}
          disabled={form.methods.length === 0}
        />
        <div className={css.grid2}>
          <Input
            label="商户号（pid）"
            mono
            autoComplete="off"
            placeholder={editing?.has_credentials ? '留空表示不改' : undefined}
            hint={keepHint}
            value={form.merchant_id}
            onChange={(e) => set('merchant_id', e.target.value)}
            error={errors.merchant_id}
          />
          <Input
            label="商户密钥"
            type="password"
            mono
            autoComplete="new-password"
            placeholder={editing?.has_credentials ? '留空表示不改' : undefined}
            hint={keepHint}
            value={form.key}
            onChange={(e) => set('key', e.target.value)}
            error={errors.key}
          />
        </div>
        <Checkbox
          label="允许内网或 http 地址（仅开发环境，生产会被拒绝）"
          checked={form.allow_private_host}
          onChange={(e) => set('allow_private_host', e.target.checked)}
        />
        {errors.allow_private_host && <span className={`${css.small} ${css.tone_danger}`}>{errors.allow_private_host}</span>}
        <p className={css.small}>
          保存前会按渠道规则试连一次地址（须为 https 公网地址）。商户号与密钥加密保存、不回显。改动在门户结账页最多 5 分钟后生效（各网关的渠道缓存）。
          {editing ? '' : '新建的渠道先处于「暂停收新单」，核对无误后在卡片上打开。'}
        </p>
      </div>
    </Modal>
  )
}
