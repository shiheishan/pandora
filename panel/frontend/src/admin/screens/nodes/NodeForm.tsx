/**
 * [INPUT]: 依赖 react 的 useMemo / useState / FormEvent，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/api 的 isApiError，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic、./queries、./schemas，依赖 ./nodes.module.css
 * [OUTPUT]: 对外提供 NodeForm（新建与编辑共用的节点表单）
 * [POS]: admin/screens/nodes 的节点表单：上半「基本信息」（后端可编辑字段全集：名称、展示名、服务器、资源池、协议、地址、端口、内核、倍率、国家），下半按 GET v1/node-protocol-schemas 渲染协议参数（必填、敏感、枚举 / 数字 / 布尔 / JSON）。新建 POST v1/nodes（幂等 node_create），编辑 PATCH v1/nodes/{id} 只发改了的字段；协议整体替换且读接口不回显敏感值，所以协议改动而敏感字段留空时先确认再清空
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useMemo, useState, type FormEvent } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Checkbox, ConfirmModal, Input, Select, TextArea, useToast } from '../../../ui'
import {
  KERNELS,
  REALITY_KEYS,
  basicFromRow,
  blankSensitive,
  createBody,
  emptyBasic,
  isStable,
  mapProtocolErrors,
  optionLabel,
  patchBody,
  protocolChanged,
  protocolFields,
  protocolLabel,
  realityEnabled,
  toFormValues,
  toProtocolConfig,
  validateBasic,
  type BasicForm,
  type FormValues,
  type ProtocolField,
} from './logic'
import css from './nodes.module.css'
import { endsIntent, useCan, useFailure, useIntentKey, useInvalidateNodes, usePools, useProtocolSchemas, useServers } from './queries'
import { adminNodeSchema, realityKeypairResponse, type AdminNode, type NodeRow, type ProtocolSchema } from './schemas'

export function NodeForm({ node, onSaved, onCancel }: { node: NodeRow | null; onSaved: (saved: AdminNode) => void; onCancel?: () => void }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const schemas = useProtocolSchemas()
  const servers = useServers()
  const pools = usePools()
  const creating = node === null
  const writable = can(creating ? 'node.provision' : 'node.write')

  const [basic, setBasic] = useState<BasicForm>(() => (node ? basicFromRow(node) : emptyBasic()))
  const schema = schemas.data?.find((s) => s.node_type === basic.nodeType)
  const fields = useMemo(() => (schema ? protocolFields(schema) : []), [schema])
  // 初值按「载入时的协议」算；换协议后表单清空，差量判断以空为基准
  const initial = useMemo(() => (node && schema && node.node_type === schema.node_type ? toFormValues(fields, node.protocol_config) : {}), [node, schema, fields])
  const [values, setValues] = useState<FormValues | null>(null)
  const current = values ?? initial
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [confirmBlank, setConfirmBlank] = useState<string[] | null>(null)

  const set = <K extends keyof BasicForm>(key: K, v: BasicForm[K]) => setBasic((b) => ({ ...b, [key]: v }))
  const setValue = (path: string, v: string) => setValues({ ...current, [path]: v })

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) =>
      creating ? api.post('v1/nodes', adminNodeSchema, { body, idempotencyKey: intent.keyFor(body) }) : api.request(`v1/nodes/${node.id}`, adminNodeSchema, { method: 'PATCH', body }),
    onSuccess: (saved) => {
      intent.reset()
      setValues(null)
      void invalidate()
      saved.warnings?.forEach((w) => toast(w, 'danger'))
      if (!creating) toast('已保存，自动下发到在线节点')
      onSaved(saved)
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      if (isApiError(error) && error.status === 422 && Object.keys(error.fields).length) {
        const { byField, rest } = mapProtocolErrors(fields, error.fields)
        setErrors({ ...rest, ...byField })
        if (Object.keys(rest).some((k) => !BASIC_KEYS.includes(k))) toast(error.message, 'danger')
        return
      }
      if (isApiError(error, 'conflict') && error.fields.row_version) {
        toast('节点已被其他人修改，已刷新到最新；请确认后再保存', 'danger')
        void invalidate()
        return
      }
      fail(error)
    },
  })

  const submit = (event?: FormEvent, confirmed = false) => {
    event?.preventDefault()
    const basicErrors = validateBasic(basic, creating)
    const { config, errors: protoErrors } = toProtocolConfig(fields, current, schema?.property_types)
    if (!schema && basic.nodeType) basicErrors.node_type = '这是旧协议，请重新选择协议'
    const all = { ...basicErrors, ...protoErrors }
    if (Object.keys(all).length) return setErrors(all)
    setErrors({})
    if (creating) return save.mutate(createBody(basic, config))
    const changed = protocolChanged(fields, initial, current) || basic.nodeType !== node.node_type
    const blanks = changed ? blankSensitive(fields, current) : []
    if (blanks.length && !confirmed) return setConfirmBlank(blanks)
    const body = patchBody(node, basic, { changed, config })
    if (Object.keys(body).length === 1) return toast('没有改动')
    save.mutate(body)
  }

  const reality = useMutation({
    mutationFn: () => api.post('v1/nodes/reality-keypair', realityKeypairResponse),
    onSuccess: (k) => {
      setValues({ ...current, [REALITY_KEYS.private_key]: k.private_key, [REALITY_KEYS.public_key]: k.public_key, [REALITY_KEYS.short_id]: k.short_id })
      toast('已生成新密钥对，保存后生效')
    },
    onError: (error) => fail(error),
  })

  const stable = (schemas.data ?? []).filter(isStable)
  const legacy = node && node.node_type && !stable.some((s) => s.node_type === node.node_type)
  const disabled = !writable || save.isPending

  return (
    <form className={css.form} onSubmit={submit} noValidate>
      <section className={css.formSection}>
        <h4 className={css.sectionTitle}>基本信息</h4>
        <div className={css.grid2}>
          <Input label="名称" value={basic.name} onChange={(e) => set('name', e.target.value)} error={errors.name} disabled={disabled} maxLength={120} data-autofocus="" />
          <Input label="展示名（用户看到的名字，可选）" value={basic.displayName} onChange={(e) => set('displayName', e.target.value)} error={errors.display_name} disabled={disabled} />
          {creating ? (
            <Select
              label="服务器"
              placeholder={servers.isPending ? '加载中…' : '选择服务器'}
              value={basic.serverId}
              onChange={(e) => set('serverId', e.target.value)}
              error={errors.server_id ?? errors.capacity_nodes}
              options={(servers.data ?? []).map((s) => ({ value: s.id, label: `${s.name}${s.region ? ` · ${s.region}` : ''}（${s.node_count}/${s.capacity_nodes}）`, disabled: s.status !== 'ready' && s.status !== 'draft' }))}
            />
          ) : (
            <Input label="服务器（换机器用「操作」里的迁移或复制）" value={node.server_name ?? '未绑定'} disabled readOnly />
          )}
          <Select
            label="资源池（可选）"
            placeholder="不加入资源池"
            value={basic.poolId}
            onChange={(e) => set('poolId', e.target.value)}
            error={errors.pool_id}
            disabled={disabled}
            options={(pools.data ?? []).map((p) => ({ value: p.id, label: p.name, disabled: p.status === 'disabled' }))}
          />
          <Select
            label="协议"
            placeholder="选择协议"
            value={basic.nodeType}
            onChange={(e) => {
              set('nodeType', e.target.value)
              setValues({})
            }}
            error={errors.node_type ?? (legacy ? '这是旧协议（只读兼容），保存前请重新选择协议' : undefined)}
            disabled={disabled}
            options={[
              ...(legacy ? [{ value: node.node_type!, label: protocolLabel(node.node_type), disabled: true }] : []),
              ...stable.map((s) => ({ value: s.node_type, label: protocolLabel(s.node_type) })),
            ]}
          />
          <Select label="内核" value={basic.kernel} onChange={(e) => set('kernel', e.target.value)} error={errors.kernel} disabled={disabled} options={KERNELS.map(([value, label]) => ({ value, label }))} />
          <Input label="地址（IP 或主机名）" mono value={basic.host} onChange={(e) => set('host', e.target.value)} error={errors.server_host} disabled={disabled} />
          <Input label="端口" mono inputMode="numeric" value={basic.port} onChange={(e) => set('port', e.target.value)} error={errors.server_port} disabled={disabled} />
          <Input label="流量倍率" mono inputMode="decimal" value={basic.rate} onChange={(e) => set('rate', e.target.value)} error={errors.traffic_rate} disabled={disabled} />
          <Input label="国家（两位代码，仅后台可见）" mono maxLength={2} value={basic.country} onChange={(e) => set('country', e.target.value.toUpperCase())} error={errors.country_code} disabled={disabled} placeholder="如 HK" />
        </div>
      </section>

      <section className={css.formSection}>
        <div className={css.sectionHead}>
          <h4 className={css.sectionTitle}>协议参数</h4>
          {schema && <span className={css.faint}>schema v{schema.version} · 字段来自后端协议定义</span>}
        </div>
        {!basic.nodeType ? (
          <div className={css.faint}>先选择协议。</div>
        ) : !schema ? (
          <div className={css.faint}>{schemas.isPending ? '正在读取协议定义…' : '没有这个协议的定义。'}</div>
        ) : (
          <>
            {fields.some((f) => f.sensitive) && !creating && <div className={css.notice}>敏感字段（标「敏感」）读接口不回显。改动协议参数会整体替换已存配置，留空的敏感字段会被清空；只改基本信息不受影响。</div>}
            <ProtocolFields schema={schema} fields={fields} values={current} errors={errors} disabled={disabled} onChange={setValue} />
            {realityEnabled(basic.nodeType, current) && (
              <div>
                <Button size="sm" busy={reality.isPending} disabled={disabled} onClick={() => reality.mutate()}>
                  生成 REALITY 密钥对
                </Button>
              </div>
            )}
          </>
        )}
      </section>

      {writable && (
        <div className={css.formActions}>
          {onCancel && (
            <Button onClick={onCancel} disabled={save.isPending}>
              取消
            </Button>
          )}
          <Button type="submit" variant="primary" busy={save.isPending}>
            {creating ? '创建草稿节点' : '保存（自动下发到在线节点）'}
          </Button>
        </div>
      )}

      <ConfirmModal
        open={confirmBlank !== null}
        title="清空这些敏感字段？"
        body={`协议参数会整体替换，下面这些敏感字段留空，保存后会被清掉：${(confirmBlank ?? []).join('、')}。要保留就先重新填写。`}
        confirmLabel="清空并保存"
        tone="danger"
        onConfirm={() => {
          setConfirmBlank(null)
          submit(undefined, true)
        }}
        onCancel={() => setConfirmBlank(null)}
      />
    </form>
  )
}

const BASIC_KEYS = ['name', 'display_name', 'server_id', 'pool_id', 'node_type', 'server_host', 'server_port', 'kernel', 'traffic_rate', 'country_code', 'capacity_nodes']

function ProtocolFields({
  schema,
  fields,
  values,
  errors,
  disabled,
  onChange,
}: {
  schema: ProtocolSchema
  fields: readonly ProtocolField[]
  values: FormValues
  errors: Record<string, string>
  disabled: boolean
  onChange: (path: string, value: string) => void
}) {
  if (!fields.length) return <div className={css.faint}>这个协议没有可配置的参数。</div>
  return (
    <div className={css.grid2}>
      {fields.map((f) => {
        const label = (
          <>
            <span className={css.mono}>{f.path}</span>
            {f.required && <span className={css.required}> *</span>}
            {f.sensitive && <span className={css.sensitive}>敏感</span>}
          </>
        )
        const value = values[f.path] ?? ''
        if (f.kind === 'enum') {
          return (
            <Select
              key={f.path}
              label={label}
              placeholder={f.required ? '选择' : '不设置'}
              value={value}
              error={errors[f.path]}
              disabled={disabled}
              onChange={(e) => onChange(f.path, e.target.value)}
              options={f.options.map((o) => ({ value: o, label: optionLabel(f.path, o) }))}
            />
          )
        }
        if (f.kind === 'boolean') {
          return (
            <div key={f.path} className={css.checkField}>
              <Checkbox label={label} checked={value === 'true'} disabled={disabled} onChange={(e) => onChange(f.path, e.target.checked ? 'true' : '')} />
              {errors[f.path] && <span className={css.error}>{errors[f.path]}</span>}
            </div>
          )
        }
        if (f.kind === 'json') {
          return <TextArea key={f.path} label={label} mono rows={3} value={value} error={errors[f.path]} disabled={disabled} onChange={(e) => onChange(f.path, e.target.value)} fieldClassName={css.span2} placeholder="JSON" />
        }
        return (
          <Input
            key={f.path}
            label={label}
            mono
            type={f.sensitive ? 'password' : 'text'}
            autoComplete={f.sensitive ? 'new-password' : 'off'}
            inputMode={f.kind === 'number' || schema.property_types?.[f.path] === 'number' ? 'decimal' : undefined}
            value={value}
            error={errors[f.path]}
            disabled={disabled}
            placeholder={f.sensitive ? '不回显，留空即不设置' : undefined}
            onChange={(e) => onChange(f.path, e.target.value)}
            fieldClassName={f.path.endsWith('private_key') || f.path.endsWith('public_key') ? css.span2 : undefined}
          />
        )
      })}
    </div>
  )
}
