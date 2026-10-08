import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { noContent } from '../../../core/api'
import { formatDateTime } from '../../../core/format'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, Modal, QueryView, Select, Table, Tag, useToast, type TableColumn } from '../../../ui'
import {
  createCredentialBody,
  credentialFormFrom,
  emptyCredentialForm,
  patchCredentialBody,
  PROVIDER_META,
  PROVIDER_OPTIONS,
  VERIFY_VIEW,
  type CredentialForm,
} from './logic'
import { useCan, useDnsCredentials, useFailure, useIntentKey, useInvalidateCerts } from './queries'
import { verifyResultSchema, type DnsCredential, type Provider, type VerifyResult } from './schemas'
import css from './certs.module.css'

const columns: TableColumn<DnsCredential>[] = [
  { key: 'name', header: '名称', width: '18%', render: (c) => <span className={css.strong}>{c.name}</span> },
  { key: 'provider', header: '服务商', width: '14%', render: (c) => PROVIDER_META[c.provider].label },
  { key: 'zone', header: '域名', render: (c) => <span className={css.mono}>{c.zone}</span> },
  { key: 'hint', header: '密钥', width: '110px', render: (c) => <span className={`${css.mono} ${css.muted}`}>{c.secret_hint ? `****${c.secret_hint}` : '****'}</span> },
  {
    key: 'verify',
    header: '校验',
    width: '22%',
    render: (c) => {
      const v = VERIFY_VIEW[c.verify_status]
      return (
        <span className={css.cellStack}>
          <Tag tone={v.tone}>{v.label}</Tag>
          {c.verify_status === 'error' && c.verify_error && (
            <span className={`${css.dangerText} ${css.ellipsis}`} title={c.verify_error}>
              {c.verify_error}
            </span>
          )}
          {c.verify_status === 'ok' && c.visible_zones !== null && c.visible_zones > 1 && <span className={css.faint}>能看到 {c.visible_zones} 个域名，不是最小权限</span>}
          {c.verified_at && c.verify_status === 'ok' && <span className={css.faint}>{formatDateTime(c.verified_at)}</span>}
        </span>
      )
    },
  },
  { key: 'certs', header: '证书', width: '64px', align: 'right', render: (c) => c.certificate_count },
]

export function DnsTab() {
  const creds = useDnsCredentials()
  const can = useCan()
  const [editing, setEditing] = useState<DnsCredential | 'new' | null>(null)
  const writable = can('node.certificate.write')
  return (
    <div className={css.stack}>
      <div className={css.toolbar}>
        <span className={css.faint}>签证书时面板用这些凭据在域名下临时写一条 _acme-challenge TXT 记录，验证完就删。凭据只能写入，保存后不再显示。</span>
        <span className={css.spacer} />
        {writable && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            添加凭据
          </Button>
        )}
      </div>
      <QueryView query={creds} rows={4} empty={<Empty title="还没有 DNS 凭据" description="支持 Cloudflare、阿里云 DNS、腾讯云 DNSPod。添加时会按服务商给出最小权限的做法。" />}>
        {(list) => (
          <div className={css.panel}>
            <Table columns={columns} rows={list} rowKey={(c) => c.id} label="DNS 凭据" onRowClick={writable ? (c) => setEditing(c) : undefined} />
          </div>
        )}
      </QueryView>
      {editing && <CredentialModal saved={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function CredentialModal({ saved: initial, onClose }: { saved: DnsCredential | null; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateCerts()
  // 新建成功后弹窗留着显示校验结论，之后的保存都是对这条凭据的修改
  const [saved, setSaved] = useState<DnsCredential | null>(initial)
  const [form, setForm] = useState<CredentialForm>(() => (initial ? credentialFormFrom(initial) : emptyCredentialForm()))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [result, setResult] = useState<VerifyResult | null>(null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const meta = PROVIDER_META[form.provider]
  const edit = (patch: Partial<CredentialForm>) => setForm((f) => ({ ...f, ...patch }))

  const done = (r: VerifyResult) => {
    intent.reset()
    setResult(r)
    setSaved(r.credential)
    setForm(credentialFormFrom(r.credential))
    void invalidate()
    if (r.ok) toast(r.resumed > 0 ? `校验通过，${r.resumed} 张证书已恢复自动签发` : '校验通过')
  }
  const save = useMutation({
    mutationFn: (body: object) =>
      saved
        ? api.request(`v1/dns-credentials/${saved.id}`, verifyResultSchema, { method: 'PATCH', body })
        : api.post('v1/dns-credentials', verifyResultSchema, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: done,
    onError: (e) => fail(e, { fields: setErrors, intent }),
  })
  const verify = useMutation({
    mutationFn: () => api.post(`v1/dns-credentials/${saved!.id}/verify`, verifyResultSchema),
    onSuccess: done,
    onError: (e) => fail(e),
  })
  const remove = useMutation({
    mutationFn: () => api.delete(`v1/dns-credentials/${saved!.id}`, noContent),
    onSuccess: () => {
      toast('凭据已删除')
      void invalidate()
      onClose()
    },
    onError: (e) => fail(e),
  })

  const submit = () => {
    if (saved) {
      const body = patchCredentialBody(form, saved)
      if (!body) return onClose()
      setErrors({})
      return save.mutate(body)
    }
    const out = createCredentialBody(form)
    if ('errors' in out) return setErrors(out.errors)
    setErrors({})
    save.mutate(out.body)
  }

  const shown = saved
  return (
    <Modal
      open
      onClose={onClose}
      title={saved ? `DNS 凭据 · ${saved.name}` : '添加 DNS 凭据'}
      size="md"
      actions={
        <>
          {saved && (
            <Button variant="danger" disabled={saved.certificate_count > 0} title={saved.certificate_count > 0 ? '还有证书在用这个凭据' : undefined} onClick={() => setConfirmDelete(true)}>
              删除
            </Button>
          )}
          <span className={css.spacer} />
          {shown && (
            <Button busy={verify.isPending} onClick={() => verify.mutate()}>
              重新校验
            </Button>
          )}
          <Button onClick={onClose}>{result ? '完成' : '取消'}</Button>
          <Button variant="primary" busy={save.isPending} onClick={submit}>
            {saved ? '保存并校验' : '添加并校验'}
          </Button>
        </>
      }
    >
      <div className={css.stack}>
        <div className={css.form}>
          {saved ? (
            <Input label="服务商" value={meta.label} disabled />
          ) : (
            <Select label="服务商" value={form.provider} options={PROVIDER_OPTIONS} onChange={(e) => edit({ provider: e.target.value as Provider, secret: {} })} />
          )}
          <Input label="名称" value={form.name} placeholder="例如 主域名" error={errors.name} onChange={(e) => edit({ name: e.target.value })} />
          <Input
            label="域名（zone）"
            fieldClassName={css.span2}
            mono
            value={form.zone}
            placeholder="example.com"
            hint="凭据管理的根域名，校验时确认凭据看得到它、能在它下面写 TXT 记录"
            error={errors.zone}
            onChange={(e) => edit({ zone: e.target.value })}
          />
          {meta.fields.map((f) => (
            <Input
              key={f.key}
              label={f.label}
              fieldClassName={meta.fields.length === 1 ? css.span2 : undefined}
              type="password"
              autoComplete="off"
              mono
              value={form.secret[f.key] ?? ''}
              placeholder={saved ? '已保存，留空不修改' : f.placeholder}
              error={errors[`secret.${f.key}`]}
              onChange={(e) => edit({ secret: { ...form.secret, [f.key]: e.target.value } })}
            />
          ))}
        </div>
        <div className={css.info}>
          <div className={css.sectionTitle}>最小权限的做法（{meta.label}）</div>
          <ol className={css.guide}>
            {meta.guide.map((g) => (
              <li key={g}>{g}</li>
            ))}
          </ol>
        </div>
        {result && <VerifyPanel result={result} />}
        {!result && shown?.verify_status === 'error' && shown.verify_error && <div className={css.alarm}>上次校验失败：{shown.verify_error}</div>}
      </div>
      <ConfirmModal
        open={confirmDelete}
        title="删除 DNS 凭据"
        body={`删除「${saved?.name ?? ''}」，加密保存的凭据一并删除，不能恢复。`}
        confirmLabel="删除"
        tone="danger"
        onConfirm={() => remove.mutateAsync().then(() => setConfirmDelete(false), () => undefined)}
        onCancel={() => setConfirmDelete(false)}
      />
    </Modal>
  )
}

function VerifyPanel({ result }: { result: VerifyResult }) {
  return (
    <div className={result.ok ? css.info : css.alarm}>
      <div className={css.strong}>{result.ok ? '校验通过：能列出域名，也能写入并删除 TXT 记录' : `校验没通过：${result.message}`}</div>
      {result.warnings.map((w) => (
        <div key={w} className={css.faint}>
          {w}
        </div>
      ))}
    </div>
  )
}
