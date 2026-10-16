import { useMutation } from '@tanstack/react-query'
import { useId, useState, type CSSProperties, type FormEvent } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, useToast } from '../../../ui'
import { useFailure, useIntentKey, useInvalidateContent } from './queries'
import { themeSaved } from './schemas'
import { THEME_COLORS, buildThemeRequest, themeToForm, type ColorKey, type ThemeErrors, type ThemeForm, type ThemeTarget } from './theme'
import css from './theme.module.css'

export function ThemeEditor({ target, onClose }: { target: ThemeTarget | null; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const formId = useId()
  const creating = target?.kind === 'create'
  const copySource = target?.kind === 'create' ? target.from : null
  const title = !target ? '' : creating ? '新建主题' : `编辑「${target.theme.name}」`
  const subtitle =
    target?.kind === 'edit' && target.theme.is_active
      ? '这是正在使用的主题：保存后用户端刷新页面即生效'
      : copySource
        ? `基于「${copySource.name}」复制`
        : undefined
  return (
    <Modal
      open={target !== null}
      onClose={onClose}
      dismissible={!busy}
      size={480}
      divided
      title={title}
      subtitle={subtitle}
      actions={
        <>
          <Button size="dialog" disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form={formId} busy={busy}>
            {creating ? '创建主题' : '保存'}
          </Button>
        </>
      }
    >
      {target && <EditorBody key={target.kind === 'edit' ? target.theme.code : `new-${target.from?.code ?? ''}`} formId={formId} target={target} onBusy={setBusy} onClose={onClose} />}
    </Modal>
  )
}

function EditorBody({ formId, target, onBusy, onClose }: { formId: string; target: ThemeTarget; onBusy: (busy: boolean) => void; onClose: () => void }) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const intent = useIntentKey()
  const invalidate = useInvalidateContent()
  const creating = target.kind === 'create'
  const [form, setForm] = useState<ThemeForm>(() => themeToForm(target))
  const [errors, setErrors] = useState<ThemeErrors>({})
  const set = <K extends keyof ThemeForm>(key: K, value: ThemeForm[K]) => setForm((f) => ({ ...f, [key]: value }))

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/themes', themeSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onMutate: () => onBusy(true),
    onSettled: () => onBusy(false),
    onSuccess: () => {
      intent.reset()
      toast(creating ? '主题已创建，激活后用户端生效' : '主题已保存')
      void invalidate('themes')
      onClose()
    },
    onError: (error) => fail(error, { fields: setErrors, intent }),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildThemeRequest(form, target)
    if (!built.ok) {
      setErrors(built.errors)
      return
    }
    setErrors({})
    save.mutate(built.body)
  }

  return (
    <form id={formId} className={css.form} onSubmit={submit} noValidate>
      <div className={css.section}>
        <div className={css.sectionTitle}>基本</div>
        {creating && (
          <Input
            label="主题标识"
            mono
            value={form.code}
            maxLength={39}
            placeholder="pine-autumn"
            hint="创建后不能修改"
            error={errors.code}
            onChange={(e) => set('code', e.target.value)}
          />
        )}
        <Input label="主题名称" value={form.name} maxLength={60} error={errors.name} onChange={(e) => set('name', e.target.value)} />
        <Input
          label="标语（可选）"
          value={form.tagline}
          maxLength={80}
          placeholder="用户端登录页显示，最多 80 个字"
          error={errors['branding.tagline']}
          onChange={(e) => set('tagline', e.target.value)}
        />
      </div>

      <div className={css.section}>
        <div className={css.sectionTitle}>配色（浅色组）</div>
        {THEME_COLORS.map(([key, token, label]) => (
          <ColorRow key={key} colorKey={key} token={token} label={label} value={form.colors[key]} error={errors[`tokens.light.${token}`]} onChange={(v) => set('colors', { ...form.colors, [key]: v })} />
        ))}
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// 一个颜色：28×28 色块（合法 6 位十六进制时可点开取色器）+ 标签 + mono 输入框
// ---------------------------------------------------------------------------
const HEX6 = /^#[0-9a-f]{6}$/i
const toHex6 = (v: string) => (v.length === 4 ? `#${[...v.slice(1)].map((c) => c + c).join('')}` : v).toLowerCase()

function ColorRow({ colorKey, token, label, value, error, onChange }: { colorKey: ColorKey; token: string; label: string; value: string; error?: string; onChange: (value: string) => void }) {
  const id = useId()
  const v = value.trim()
  const style: CSSProperties = { background: v }
  return (
    <div className={css.colorRow}>
      {HEX6.test(v) ? (
        <label className={css.chip} style={style} title="点击取色">
          <input type="color" className={css.picker} value={toHex6(v)} aria-label={`${label} 取色`} onChange={(e) => onChange(e.target.value)} />
        </label>
      ) : (
        <span className={css.chip} style={style} aria-hidden="true" />
      )}
      <label className={css.colorLabel} htmlFor={id}>
        {label}
      </label>
      <Input
        id={id}
        className={css.colorInput}
        size="sm"
        mono
        value={value}
        placeholder={`浅色 ${token}`}
        aria-label={`${label}（${token}）`}
        error={error}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  )
}
