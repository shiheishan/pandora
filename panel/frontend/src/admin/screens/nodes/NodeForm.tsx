import { useMutation } from '@tanstack/react-query'
import { useEffect, useMemo, useState, type FormEvent, type ReactNode } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Input, Modal, Select, Switch, Tag, TextArea, useToast } from '../../../ui'
import {
  BASIC_STEP_KEYS,
  PROTOCOL_ORDER,
  REALITY_KEYS,
  REGIONS,
  basicFromRow,
  clearableSecret,
  clearedSecrets,
  createBody,
  emptyBasic,
  enumOptions,
  isStable,
  mapProtocolErrors,
  patchBody,
  protocolChanged,
  protocolDefaults,
  protocolFields,
  protocolKind,
  protocolLabel,
  protocolNotices,
  realityEnabled,
  serverAcceptsNodes,
  toFormValues,
  toProtocolConfig,
  UNBOUND_HINT,
  validateBasic,
  visibleFields,
  withProtocolDefaults,
  type BasicForm,
  type FormValues,
  type ProtocolField,
} from './logic'
import css from './form.module.css'
import { endsIntent, useFailure, useIntentKey, useInvalidateNodes, usePools, useProtocolSchemas, useServers } from './queries'
import { adminNodeSchema, realityKeypairResponse, type AdminNode, type NodeDetail, type ProtocolSchema } from './schemas'

const REGION_CODES = new Set(REGIONS.map(([c]) => c))
const REGION_OTHER = '__other'

function basicDirty(row: NodeDetail, b: BasicForm): boolean {
  const before = basicFromRow(row)
  return (
    b.name.trim() !== before.name ||
    b.nodeType !== before.nodeType ||
    b.host.trim() !== before.host ||
    b.port !== before.port ||
    Number(b.rate) !== row.traffic_rate ||
    b.country.trim().toUpperCase() !== before.country
  )
}

function fieldLabel(f: ProtocolField): ReactNode {
  return (
    <>
      <span>{f.label}</span>
      {f.required && <span className={css.required}>*</span>}
      {f.sensitive && <span className={css.sensitive}>敏感</span>}
    </>
  )
}

function ParamRows({
  nodeType,
  schema,
  fields,
  values,
  errors,
  disabled,
  onChange,
  creating,
  keepSecrets = false,
  cleared = new Set<string>(),
  onToggleClear,
  onGenerateReality,
  realityPending,
  showAdvanced,
  onToggleAdvanced,
}: {
  nodeType: string
  schema: ProtocolSchema
  fields: readonly ProtocolField[]
  values: FormValues
  errors: Record<string, string>
  disabled: boolean
  onChange: (path: string, v: string) => void
  creating: boolean
  keepSecrets?: boolean
  cleared?: ReadonlySet<string>
  onToggleClear?: (path: string) => void
  onGenerateReality?: () => void
  realityPending?: boolean
  showAdvanced: boolean
  onToggleAdvanced: () => void
}) {
  const visible = visibleFields(fields, values)
  const advancedCount = visible.filter((f) => f.advanced).length
  const rows = visible.filter((f) => showAdvanced || !f.advanced)
  const notices = protocolNotices(nodeType, values)

  if (!fields.length) return <div className={css.faint}>这个协议没有可配置的参数。</div>

  return (
    <div className={css.paramBlock}>
      <div className={css.paramHead}>
        <h4 className={css.paramTitle}>{protocolLabel(nodeType)} 参数</h4>
        {schema.note && <p className={css.paramNote}>{schema.note}</p>}
        {notices.map((n) => (
          <div key={n} className={css.notice}>{n}</div>
        ))}
      </div>
      {rows.map((f) => {
        const value = values[f.path] ?? ''
        const clearing = f.sensitive && cleared.has(f.path) && !value
        const listPh = f.kind === 'list' ? '可填多个，逗号分隔' : undefined
        const sensitivePh = creating
          ? undefined
          : clearing
            ? '保存时清空'
            : keepSecrets
              ? '已保存 · 留空保留原值'
              : undefined

        if (f.kind === 'enum') {
          return (
            <div key={f.path} className={css.paramRow}>
              <div className={css.paramLabel}>{fieldLabel(f)}</div>
              <div className={css.paramControl}>
                <Select
                  aria-label={f.label}
                  placeholder={f.required ? '选择' : undefined}
                  value={value}
                  error={errors[f.path]}
                  hint={f.hint}
                  disabled={disabled}
                  onChange={(e) => onChange(f.path, e.target.value)}
                  options={enumOptions(f)}
                />
              </div>
            </div>
          )
        }
        if (f.kind === 'boolean') {
          const on = value === 'true'
          return (
            <div key={f.path} className={css.paramRow}>
              <div className={css.paramLabel}>{fieldLabel(f)}</div>
              <div className={css.paramControl}>
                <div className={css.boolRow}>
                  <Switch checked={on} disabled={disabled} aria-label={f.label} onChange={(e) => onChange(f.path, e.target.checked ? 'true' : 'false')} />
                  <span className={css.boolText}>{on ? '开' : '关'}</span>
                </div>
                {errors[f.path] && <span className={css.required}>{errors[f.path]}</span>}
                {f.hint && !errors[f.path] && <span className={css.faint}>{f.hint}</span>}
              </div>
            </div>
          )
        }
        if (f.kind === 'json') {
          return (
            <div key={f.path} className={css.paramRow}>
              <div className={css.paramLabel}>{fieldLabel(f)}</div>
              <div className={css.paramControl}>
                <TextArea
                  aria-label={f.label}
                  mono
                  rows={2}
                  value={value}
                  error={errors[f.path]}
                  hint={f.hint}
                  disabled={disabled}
                  placeholder={f.placeholder ?? 'JSON'}
                  onChange={(e) => onChange(f.path, e.target.value)}
                />
              </div>
            </div>
          )
        }

        const isPrivateKey = f.path === REALITY_KEYS.private_key
        const input = (
          <Input
            aria-label={f.label}
            mono
            type={f.sensitive ? 'password' : 'text'}
            autoComplete={f.sensitive ? 'new-password' : 'off'}
            inputMode={f.kind === 'number' || schema.property_types?.[f.path] === 'number' ? 'decimal' : undefined}
            value={value}
            error={errors[f.path]}
            disabled={disabled}
            placeholder={sensitivePh ?? f.placeholder ?? listPh}
            hint={clearing ? '保存时会清空原值' : f.hint}
            onChange={(e) => onChange(f.path, e.target.value)}
          />
        )

        return (
          <div key={f.path} className={css.paramRow}>
            <div className={css.paramLabel}>{fieldLabel(f)}</div>
            <div className={css.paramControl}>
              <div className={css.paramInputRow}>
                {input}
                {isPrivateKey && onGenerateReality && (
                  <Button size="xs" busy={realityPending} disabled={disabled} onClick={onGenerateReality}>
                    生成密钥对
                  </Button>
                )}
                {!creating && keepSecrets && clearableSecret(f) && onToggleClear && (
                  <Button size="xs" variant="ghost" disabled={disabled || Boolean(value)} onClick={() => onToggleClear(f.path)}>
                    {clearing ? '撤回清空' : '清空'}
                  </Button>
                )}
              </div>
            </div>
          </div>
        )
      })}
      {advancedCount > 0 && (
        <button type="button" className={css.advToggle} onClick={onToggleAdvanced}>
          {showAdvanced ? '收起高级参数' : `显示高级参数（${advancedCount} 项）`}
        </button>
      )}
    </div>
  )
}

export function NodeCreateWizard({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (saved: AdminNode) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const intent = useIntentKey()
  const schemas = useProtocolSchemas()
  const servers = useServers()
  const pools = usePools()

  const stable = useMemo(() => (schemas.data ?? []).filter(isStable), [schemas.data])
  const defaultType = useMemo(() => PROTOCOL_ORDER.find((t) => stable.some((s) => s.node_type === t)) ?? stable[0]?.node_type ?? '', [stable])

  const [step, setStep] = useState(0)
  const [basic, setBasic] = useState<BasicForm>(() => emptyBasic())
  const [values, setValues] = useState<FormValues>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [countryCustom, setCountryCustom] = useState(false)
  const [showAdvanced, setShowAdvanced] = useState(false)

  const schema = stable.find((s) => s.node_type === basic.nodeType)
  const fields = useMemo(() => (schema ? protocolFields(schema) : []), [schema])
  const current = values

  // 每次打开由调用方换 key 重挂载，状态从头开始。协议定义到了、还没选协议时，
  // 在渲染期预选第一个并填上它的默认值（React 文档「根据前一次渲染调整状态」的写法，不在 effect 里 setState）
  if (open && !basic.nodeType && defaultType) {
    const sch = stable.find((s) => s.node_type === defaultType)
    setBasic({ ...basic, nodeType: defaultType })
    setValues(protocolDefaults(sch ? protocolFields(sch) : []))
  }

  const setBasicKey = <K extends keyof BasicForm>(key: K, v: BasicForm[K]) => setBasic((b) => ({ ...b, [key]: v }))
  const setValue = (path: string, v: string) => {
    setValues(withProtocolDefaults(basic.nodeType, path, { ...current, [path]: v }))
    setErrors((e) => {
      const next = { ...e }
      delete next[path]
      return next
    })
  }

  const pickProto = (nodeType: string) => {
    const sch = stable.find((s) => s.node_type === nodeType)
    const flds = sch ? protocolFields(sch) : []
    setBasic((b) => ({ ...b, nodeType }))
    setValues(protocolDefaults(flds))
    setErrors({})
    setShowAdvanced(false)
  }

  const visible = visibleFields(fields, current)
  const reqCount = visible.filter((f) => f.required).length

  const stepHasErr = (i: number) => {
    if (i === 1) return BASIC_STEP_KEYS.some((k) => errors[k])
    if (i === 2) return Object.keys(errors).some((k) => !BASIC_STEP_KEYS.includes(k as (typeof BASIC_STEP_KEYS)[number]))
    return false
  }

  const stepSummary = (i: number) => {
    if (stepHasErr(i)) return '有待修正的项'
    if (i === 0) return protocolLabel(basic.nodeType)
    if (i === 1) {
      if (!basic.name.trim() && !basic.host.trim()) return '未填写'
      const host = basic.host.trim()
      const addr = host ? (basic.port.trim() ? `${host}:${basic.port.trim()}` : host) : ''
      return basic.name.trim() ? `${basic.name.trim()}${addr ? ` · ${addr}` : ''}` : '未填写'
    }
    return visible.length ? `${visible.length} 项${reqCount ? `，${reqCount} 项必填` : ''}` : '—'
  }

  const regionSelectValue = countryCustom || (basic.country && !REGION_CODES.has(basic.country)) ? REGION_OTHER : basic.country

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('v1/nodes', adminNodeSchema, { body, idempotencyKey: intent.keyFor(body) }),
    onSuccess: (saved) => {
      intent.reset()
      void invalidate()
      saved.warnings?.forEach((w) => toast(w, 'danger'))
      onCreated(saved)
    },
    onError: (error) => {
      if (endsIntent(error)) intent.reset()
      if (isApiError(error) && error.status === 422 && Object.keys(error.fields).length) {
        const { byField, rest } = mapProtocolErrors(fields, error.fields)
        const all = { ...rest, ...byField }
        setErrors(all)
        if (BASIC_STEP_KEYS.some((k) => all[k])) setStep(1)
        else setStep(2)
        if (Object.keys(rest).some((k) => !BASIC_STEP_KEYS.includes(k as (typeof BASIC_STEP_KEYS)[number]))) toast(error.message, 'danger')
        return
      }
      if (isApiError(error, 'conflict') && error.fields.server_port) {
        setErrors({ server_port: error.fields.server_port })
        setStep(1)
        return
      }
      if (isApiError(error, 'conflict') && /名称/.test(error.message)) {
        setErrors({ name: error.message })
        setStep(1)
        return
      }
      fail(error, { intent })
    },
  })

  const reality = useMutation({
    mutationFn: () => api.post('v1/nodes/reality-keypair', realityKeypairResponse),
    onSuccess: (k) => {
      setValues({
        ...current,
        [REALITY_KEYS.private_key]: k.private_key,
        [REALITY_KEYS.public_key]: k.public_key,
        [REALITY_KEYS.short_id]: k.short_id,
      })
      toast('已生成 REALITY 密钥对，私钥保存后不再回显')
    },
    onError: (error) => fail(error),
  })

  const goNext = () => {
    if (step === 0) return setStep(1)
    if (step === 1) {
      const all = validateBasic(basic, true)
      const stepErr = Object.fromEntries(Object.entries(all).filter(([k]) => BASIC_STEP_KEYS.includes(k as (typeof BASIC_STEP_KEYS)[number])))
      if (Object.keys(stepErr).length) return setErrors(stepErr)
      setErrors({})
      return setStep(2)
    }
  }

  const submitCreate = () => {
    const basicErrors = validateBasic(basic, true)
    const { config, errors: protoErrors } = toProtocolConfig(fields, current, schema?.property_types)
    const all = { ...basicErrors, ...protoErrors }
    if (Object.keys(all).length) {
      setErrors(all)
      if (BASIC_STEP_KEYS.some((k) => all[k])) setStep(1)
      else setStep(2)
      return
    }
    save.mutate(createBody(basic, config))
  }

  const disabled = save.isPending
  const footNotes = ['第 1 步，共 3 步', '第 2 步，共 3 步', '创建后在节点详情右上角上线']

  return (
    <Modal
      open={open}
      onClose={onClose}
      size={760}
      divided
      dismissible={!save.isPending}
      className={css.wizardPanel}
      bodyClassName={css.wizardBody}
      title="新建节点"
      headExtra={<Tag tone="warn">草稿 · 上线后才下发</Tag>}
      footerStart={<span className={css.faint}>{footNotes[step]}</span>}
      actions={
        <>
          <Button size="dialog" onClick={step === 0 ? onClose : () => setStep(step - 1)} disabled={disabled}>
            {step === 0 ? '取消' : '上一步'}
          </Button>
          {step < 2 ? (
            <Button size="dialog" variant="primary" onClick={goNext} disabled={disabled || !basic.nodeType}>
              下一步
            </Button>
          ) : (
            <Button size="dialog" variant="primary" busy={save.isPending} onClick={submitCreate} disabled={disabled}>
              创建草稿
            </Button>
          )}
        </>
      }
    >
      <nav className={css.stepNav} aria-label="新建步骤">
        {['协议', '连接信息', '协议参数'].map((label, i) => {
          const err = stepHasErr(i)
          const done = i < step && !err
          return (
            <button
              key={label}
              type="button"
              className={`${css.stepBtn} ${i === step ? css.stepBtnCurrent : ''}`}
              onClick={() => setStep(i)}
            >
              <span
                className={`${css.stepDot} ${err ? css.stepDotErr : ''} ${done ? css.stepDotDone : ''} ${i === step && !err ? css.stepDotCurrent : ''}`}
              >
                {err ? '!' : done ? '✓' : i + 1}
              </span>
              <span className={css.stepText}>
                <span className={i === step ? css.stepLabelCurrent : css.stepLabel}>{label}</span>
                <span className={`${css.stepSum} ${err ? css.stepSumErr : ''}`}>{stepSummary(i)}</span>
              </span>
            </button>
          )
        })}
      </nav>
      <div className={css.stepContent}>
        {step === 0 && (
          <div>
            <div className={css.stepHead}>
              <div className={css.stepTitle}>选择协议</div>
              <div className={css.stepSub}>决定下一步要填哪些参数。</div>
            </div>
            <div className={css.protoGrid}>
              {PROTOCOL_ORDER.filter((t) => stable.some((s) => s.node_type === t)).map((t) => {
                const sch = stable.find((s) => s.node_type === t)!
                const on = basic.nodeType === t
                return (
                  <button
                    key={t}
                    type="button"
                    className={`${css.protoCard} ${on ? css.protoCardOn : ''}`}
                    onClick={() => pickProto(t)}
                  >
                    <span>{protocolLabel(t)}</span>
                    <span className={css.protoKind}>{protocolKind(sch)}</span>
                  </button>
                )
              })}
            </div>
          </div>
        )}
        {step === 1 && (
          <div>
            <div className={css.stepHead}>
              <div className={css.stepTitle}>连接信息</div>
              <div className={css.stepSub}>客户端看到的名称和连接地址。</div>
            </div>
            <div className={css.connectGrid}>
              <Input
                label="节点名称"
                hint="显示在客户端的节点列表里"
                placeholder="HK-HKG-03"
                value={basic.name}
                error={errors.name}
                disabled={disabled}
                onChange={(e) => setBasicKey('name', e.target.value)}
              />
              <div>
                <Select
                  label="地区"
                  placeholder="选择地区"
                  value={regionSelectValue}
                  error={errors.country_code}
                  disabled={disabled}
                  onChange={(e) => {
                    const v = e.target.value
                    if (v === REGION_OTHER) {
                      setCountryCustom(true)
                      setBasicKey('country', '')
                    } else {
                      setCountryCustom(false)
                      setBasicKey('country', v)
                    }
                    setErrors((er) => {
                      const next = { ...er }
                      delete next.country_code
                      return next
                    })
                  }}
                  options={[
                    ...REGIONS.map(([code, label]) => ({ value: code, label })),
                    { value: REGION_OTHER, label: '其他地区（手动输入）' },
                  ]}
                />
                {countryCustom && (
                  <Input
                    label="地区代码"
                    mono
                    maxLength={2}
                    hint="填 ISO 两位字母代码，如 MY、BR"
                    placeholder="例如 MY"
                    value={basic.country}
                    error={errors.country_code}
                    disabled={disabled}
                    onChange={(e) => setBasicKey('country', e.target.value.replace(/[^A-Za-z]/g, '').slice(0, 2).toUpperCase())}
                  />
                )}
              </div>
              <Input
                label="地址"
                hint="域名或 IP；上线前要填"
                placeholder="hk3.example.com"
                mono
                value={basic.host}
                error={errors.server_host}
                disabled={disabled}
                onChange={(e) => setBasicKey('host', e.target.value)}
              />
              <Input
                label="端口"
                hint={basic.serverId ? undefined : '暂不绑定时可以先空着，绑定服务器时要填'}
                mono
                inputMode="numeric"
                value={basic.port}
                error={errors.server_port}
                disabled={disabled}
                onChange={(e) => setBasicKey('port', e.target.value)}
              />
              <div className={css.span2}>
                <Select
                  label="所在服务器"
                  hint={basic.serverId ? '服务器还没接入的，创建后到「服务器」页签发安装令牌' : UNBOUND_HINT}
                  emptyOption="暂不绑定"
                  value={basic.serverId}
                  error={errors.server_id ?? errors.capacity_nodes}
                  disabled={disabled}
                  onChange={(e) => setBasicKey('serverId', e.target.value)}
                  options={(servers.data ?? []).map((s) => ({
                    value: s.id,
                    label: `${s.name}${s.region ? ` · ${s.region}` : ''}（${s.node_count}/${s.capacity_nodes}）`,
                    disabled: !serverAcceptsNodes(s),
                  }))}
                />
              </div>
              <div className={css.span2}>
                <Select
                  label="节点池"
                  hint="节点只服务所在节点池绑定的套餐"
                  placeholder={pools.data && pools.data.length === 0 ? '还没有节点池，先到「节点池」新建' : '选择节点池'}
                  value={basic.poolId}
                  error={errors.pool_id}
                  disabled={disabled}
                  onChange={(e) => setBasicKey('poolId', e.target.value)}
                  options={(pools.data ?? []).map((p) => ({ value: p.id, label: p.name, disabled: p.status === 'disabled' }))}
                />
              </div>
            </div>
          </div>
        )}
        {step === 2 && schema && (
          <ParamRows
            nodeType={basic.nodeType}
            schema={schema}
            fields={fields}
            values={current}
            errors={errors}
            disabled={disabled}
            creating
            showAdvanced={showAdvanced}
            onToggleAdvanced={() => setShowAdvanced((v) => !v)}
            onChange={setValue}
            onGenerateReality={realityEnabled(basic.nodeType, current) ? () => reality.mutate() : undefined}
            realityPending={reality.isPending}
          />
        )}
      </div>
    </Modal>
  )
}

export function NodeForm({ node, onSaved, onDirtyChange }: { node: NodeDetail; onSaved: (saved: AdminNode) => void; onDirtyChange?: (dirty: boolean) => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateNodes()
  const schemas = useProtocolSchemas()

  const [basic, setBasic] = useState<BasicForm>(() => basicFromRow(node))
  const schema = schemas.data?.find((s) => s.node_type === basic.nodeType)
  const fields = useMemo(() => (schema ? protocolFields(schema) : []), [schema])
  const initial = useMemo(() => (schema && node.node_type === schema.node_type ? toFormValues(fields, node.protocol_config) : {}), [node, schema, fields])
  const [values, setValues] = useState<FormValues | null>(null)
  const current = values ?? initial
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [cleared, setCleared] = useState<ReadonlySet<string>>(new Set())
  const [confirmClear, setConfirmClear] = useState<string[] | null>(null)
  const [showAdvanced, setShowAdvanced] = useState(false)

  const keepSecrets = basic.nodeType === node.node_type
  const liveCleared = keepSecrets ? cleared : new Set<string>()

  const stable = (schemas.data ?? []).filter(isStable)
  const legacy = node.node_type && !stable.some((s) => s.node_type === node.node_type)

  const dirty = basicDirty(node, basic) || protocolChanged(fields, initial, current, liveCleared, schema?.property_types)

  // 把「有未保存的修改」报给外层（详情弹窗用它决定关闭前要不要确认）
  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  const set = <K extends keyof BasicForm>(key: K, v: BasicForm[K]) => setBasic((b) => ({ ...b, [key]: v }))
  const setValue = (path: string, v: string) => {
    setValues(withProtocolDefaults(basic.nodeType, path, { ...current, [path]: v }))
    if (v && cleared.has(path)) setCleared(new Set([...cleared].filter((p) => p !== path)))
  }
  const toggleClear = (path: string) => setCleared(cleared.has(path) ? new Set([...cleared].filter((p) => p !== path)) : new Set([...cleared, path]))

  const resetForm = () => {
    setBasic(basicFromRow(node))
    setValues(null)
    setErrors({})
    setCleared(new Set())
    setShowAdvanced(false)
  }

  const save = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.request(`v1/nodes/${node.id}`, adminNodeSchema, { method: 'PATCH', body }),
    onSuccess: (saved) => {
      setValues(null)
      setCleared(new Set())
      void invalidate()
      saved.warnings?.forEach((w) => toast(w, 'danger'))
      toast('已保存，约 30 秒内下发到节点')
      onSaved(saved)
    },
    onError: (error) => {
      if (isApiError(error) && error.status === 422 && Object.keys(error.fields).length) {
        const { byField, rest } = mapProtocolErrors(fields, error.fields)
        setErrors({ ...rest, ...byField })
        if (Object.keys(rest).length) toast(error.message, 'danger')
        return
      }
      if (isApiError(error, 'conflict') && error.fields.row_version) {
        toast('节点已被其他人修改，已刷新到最新；请确认后再保存', 'danger')
        void invalidate()
        return
      }
      if (isApiError(error, 'conflict') && error.fields.server_port) {
        setErrors({ server_port: error.fields.server_port })
        return
      }
      fail(error)
    },
  })

  const reality = useMutation({
    mutationFn: () => api.post('v1/nodes/reality-keypair', realityKeypairResponse),
    onSuccess: (k) => {
      setValues({
        ...current,
        [REALITY_KEYS.private_key]: k.private_key,
        [REALITY_KEYS.public_key]: k.public_key,
        [REALITY_KEYS.short_id]: k.short_id,
      })
      toast('已生成 REALITY 密钥对，私钥保存后不再回显')
    },
    onError: (error) => fail(error),
  })

  const submit = (event?: FormEvent, confirmed = false) => {
    event?.preventDefault()
    const basicErrors = validateBasic(basic, false, node.serving_status === 'active')
    const { config, errors: protoErrors } = toProtocolConfig(fields, current, schema?.property_types, { keepSecrets, cleared: liveCleared })
    if (!schema && basic.nodeType) basicErrors.node_type = '这是旧协议，请重新选择协议'
    const all = { ...basicErrors, ...protoErrors }
    if (Object.keys(all).length) return setErrors(all)
    setErrors({})
    const changed = protocolChanged(fields, initial, current, liveCleared, schema?.property_types) || basic.nodeType !== node.node_type
    const clearing = clearedSecrets(fields, current, liveCleared)
    if (clearing.length && !confirmed) return setConfirmClear(clearing)
    const body = patchBody(node, basic, { changed, config })
    if (Object.keys(body).length === 1) return toast('没有改动')
    save.mutate(body)
  }

  const disabled = save.isPending

  return (
    <form className={css.editWrap} onSubmit={submit} noValidate>
      <section>
        <h4 className={css.editSectionTitle}>连接信息</h4>
        <div className={css.editGrid}>
          <Input label="名称" value={basic.name} onChange={(e) => set('name', e.target.value)} error={errors.name} disabled={disabled} maxLength={120} />
          <Input
            label="地区"
            mono
            maxLength={2}
            placeholder="如 HK"
            value={basic.country}
            error={errors.country_code}
            disabled={disabled}
            onChange={(e) => set('country', e.target.value.replace(/[^A-Za-z]/g, '').slice(0, 2).toUpperCase())}
          />
          <Input label="地址" mono value={basic.host} onChange={(e) => set('host', e.target.value)} error={errors.server_host} disabled={disabled} />
          <Input label="端口" mono inputMode="numeric" value={basic.port} onChange={(e) => set('port', e.target.value)} error={errors.server_port} disabled={disabled} />
          <Input label="流量倍率" mono inputMode="decimal" value={basic.rate} onChange={(e) => set('rate', e.target.value)} error={errors.traffic_rate} disabled={disabled} />
          <Input label="所在服务器" value={node.server_name ?? '未绑定'} hint="在「身份与操作」里绑定 / 改绑" disabled readOnly fieldClassName={css.span2} />
          <Select
            label="协议"
            placeholder="选择协议"
            value={basic.nodeType}
            onChange={(e) => {
              const next = stable.find((x) => x.node_type === e.target.value)
              set('nodeType', e.target.value)
              setValues(next ? protocolDefaults(protocolFields(next)) : {})
              setCleared(new Set())
            }}
            error={errors.node_type ?? (legacy ? '这是旧协议（只读兼容），保存前请重新选择协议' : undefined)}
            disabled={disabled}
            options={[
              ...(legacy && node.node_type ? [{ value: node.node_type, label: protocolLabel(node.node_type), disabled: true }] : []),
              ...stable.map((s) => ({ value: s.node_type, label: protocolLabel(s.node_type) })),
            ]}
          />
        </div>
      </section>

      <section>
        {!basic.nodeType ? (
          <div className={css.faint}>先选择协议。</div>
        ) : !schema ? (
          <div className={css.faint}>{schemas.isPending ? '正在读取协议定义…' : '没有这个协议的定义。'}</div>
        ) : (
          <>
            {fields.some((f) => f.sensitive) && (
              <div className={css.secretNote}>
                {keepSecrets
                  ? '敏感字段（标「敏感」）读接口不回显：留空保存会保留原值，要改就填新值；选填的敏感字段可以点「清空」删掉。'
                  : '换了协议，原来的敏感字段不会带过来：必填的敏感字段要重新填写。'}
              </div>
            )}
            <ParamRows
              nodeType={basic.nodeType}
              schema={schema}
              fields={fields}
              values={current}
              errors={errors}
              disabled={disabled}
              creating={false}
              keepSecrets={keepSecrets}
              cleared={liveCleared}
              onToggleClear={toggleClear}
              showAdvanced={showAdvanced}
              onToggleAdvanced={() => setShowAdvanced((v) => !v)}
              onChange={setValue}
              onGenerateReality={realityEnabled(basic.nodeType, current) ? () => reality.mutate() : undefined}
              realityPending={reality.isPending}
            />
          </>
        )}
      </section>

      <div className={css.stickyFoot}>
        <span className={css.footHint}>{dirty ? '有未保存的修改' : '保存后约 30 秒内下发到节点'}</span>
        <div className={css.footActions}>
          <Button type="button" onClick={resetForm} disabled={!dirty || disabled}>还原</Button>
          <Button type="submit" variant="primary" busy={save.isPending} disabled={!dirty || disabled}>保存</Button>
        </div>
      </div>

      <ConfirmModal
        open={confirmClear !== null}
        title="清空这些敏感字段？"
        body={`保存后这些字段的原值会被删掉，节点下次拉配置时生效：${(confirmClear ?? []).map((p) => fields.find((f) => f.path === p)?.label ?? p).join('、')}。不想删就取消，再点一次「清空」撤回。`}
        confirmLabel="清空并保存"
        tone="danger"
        onConfirm={() => {
          setConfirmClear(null)
          submit(undefined, true)
        }}
        onCancel={() => setConfirmClear(null)}
      />
    </form>
  )
}
