import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { href } from '../../../core/router'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, QueryView, Select, TextArea, useToast } from '../../../ui'
import { identifierNotices, parseIdentifiers, PROVIDER_META } from './logic'
import { useDnsCredentials, useFailure, useIntentKey, useInvalidateCerts } from './queries'
import { certificateSchema, type Certificate, type DnsCredential, type KeyType } from './schemas'
import css from './certs.module.css'

const KEY_OPTIONS = [
  { value: 'ecdsa-p256', label: 'ECDSA P-256（推荐）' },
  { value: 'rsa-2048', label: 'RSA 2048（兼容老客户端）' },
]

/** 新建（cert 为空）或编辑证书。编辑只发改了的字段；改了域名或密钥类型会排一张新的签发单。 */
export function CertFormModal({ cert, onClose, onSaved }: { cert?: Certificate; onClose: () => void; onSaved: (c: Certificate) => void }) {
  const creds = useDnsCredentials()
  return (
    <Modal open onClose={onClose} title={cert ? '编辑证书' : '新建证书'} size="md">
      <QueryView query={creds} rows={3} empty={null}>
        {(list) =>
          list.length === 0 ? (
            <div className={css.notice}>
              还没有 DNS 凭据。先到 <a href={href('/certs/dns')}>DNS 凭据</a> 添加域名所在 DNS 服务商的凭据，再回来新建证书。
            </div>
          ) : (
            <CertForm creds={list} cert={cert} onClose={onClose} onSaved={onSaved} />
          )
        }
      </QueryView>
    </Modal>
  )
}

function CertForm({ creds, cert, onClose, onSaved }: { creds: DnsCredential[]; cert?: Certificate; onClose: () => void; onSaved: (c: Certificate) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateCerts()
  const [name, setName] = useState(cert?.name ?? '')
  const [idsText, setIdsText] = useState(cert?.identifiers.join('\n') ?? '')
  // 只有一个凭据时直接选上
  const [credId, setCredId] = useState(cert?.dns_credential_id ?? (creds.length === 1 ? creds[0]!.id : ''))
  const [keyType, setKeyType] = useState<KeyType>(cert?.key_type ?? 'ecdsa-p256')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const ids = parseIdentifiers(idsText)
  const cred = creds.find((c) => c.id === credId) ?? null
  const notices = identifierNotices(ids, cred?.zone ?? null)

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      cert
        ? api.request(`v1/certificates/${cert.id}`, certificateSchema, { method: 'PATCH', body })
        : api.post('v1/certificates', certificateSchema, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (c) => {
      intent.reset()
      toast(cert ? '证书已保存' : '证书已创建，正在排队签发（通常一两分钟）')
      void invalidate()
      onSaved(c)
    },
    onError: (e) => fail(e, { fields: setErrors, intent }),
  })

  const submit = () => {
    const bad: Record<string, string> = {}
    if (!name.trim()) bad.name = '填写名称'
    if (ids.length === 0) bad.identifiers = '至少填一个域名'
    if (!credId) bad.dns_credential_id = '选一个 DNS 凭据'
    if (Object.keys(bad).length > 0) return setErrors(bad)
    setErrors({})
    const body: Record<string, unknown> = {}
    if (!cert || name.trim() !== cert.name) body.name = name.trim()
    if (!cert || ids.join(',') !== cert.identifiers.join(',')) body.identifiers = ids
    if (!cert || credId !== cert.dns_credential_id) body.dns_credential_id = credId
    if (!cert || keyType !== cert.key_type) body.key_type = keyType
    if (cert && Object.keys(body).length === 0) return onClose()
    save.mutate(body)
  }

  return (
    <div className={css.stack}>
      <div className={css.form}>
        <Input label="名称" fieldClassName={css.span2} value={name} placeholder="例如 主域名通配符" error={errors.name} onChange={(e) => setName(e.target.value)} />
        <TextArea
          label="域名"
          fieldClassName={css.span2}
          rows={3}
          value={idsText}
          placeholder={'*.example.com\nexample.com'}
          hint="一行一个，最多 20 个；通配符只能在最左边（*.example.com 不含 example.com 本身）。不支持 IP。"
          error={errors.identifiers}
          onChange={(e) => setIdsText(e.target.value)}
        />
        <Select
          label="DNS 凭据"
          value={credId}
          placeholder="选择凭据"
          options={creds.map((c) => ({ value: c.id, label: `${c.name} · ${PROVIDER_META[c.provider].label} · ${c.zone}` }))}
          error={errors.dns_credential_id}
          onChange={(e) => setCredId(e.target.value)}
        />
        <Select label="密钥类型" value={keyType} options={KEY_OPTIONS} error={errors.key_type} onChange={(e) => setKeyType(e.target.value as KeyType)} />
        {cert && <div className={`${css.span2} ${css.faint}`}>改了域名或密钥类型会立刻排一张新的签发单；只改名称不会。</div>}
      </div>
      {notices.map((n) => (
        <div key={n} className={css.notice}>
          {n}
        </div>
      ))}
      <div className={css.toolbar}>
        <span className={css.spacer} />
        <Button onClick={onClose}>取消</Button>
        <Button variant="primary" busy={save.isPending} onClick={submit}>
          {cert ? '保存' : '创建并签发'}
        </Button>
      </div>
    </div>
  )
}
