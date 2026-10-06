import { useMutation } from '@tanstack/react-query'
import { useId, useState, type ChangeEvent, type CSSProperties, type FormEvent } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Drawer, Input, Segmented, useToast } from '../../../ui'
import { useFailure, useIntentKey, useInvalidateContent } from './queries'
import { themeSaved } from './schemas'
import { TOKEN_SECTIONS, buildThemeRequest, firstErrorMode, isShadowToken, logoProblem, modeErrorCount, previewVars, themeToForm, type ThemeErrors, type ThemeForm, type ThemeMode, type ThemeTarget } from './theme'
import css from './theme.module.css'

const MODE_LABEL: Readonly<Record<ThemeMode, string>> = { light: '浅色', dark: '深色' }

export function ThemeEditor({ target, onClose }: { target: ThemeTarget | null; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const formId = useId()
  const title = !target ? '' : target.kind === 'edit' ? `编辑「${target.theme.name}」` : target.from?.is_builtin ? `另存为新主题（基于「${target.from.name}」）` : '新建主题'
  return (
    <Drawer
      open={target !== null}
      onClose={onClose}
      dismissible={!busy}
      title={title}
      subtitle={target?.kind === 'edit' && target.theme.is_active ? '这是正在使用的主题：保存后门户刷新页面即生效' : undefined}
      width={1040}
      actions={
        <>
          <Button size="dialog" disabled={busy} onClick={onClose}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form={formId} busy={busy}>
            {target?.kind === 'edit' ? '保存修改' : '保存新主题'}
          </Button>
        </>
      }
    >
      {target && <EditorBody key={target.kind === 'edit' ? target.theme.code : `new-${target.from?.code ?? ''}`} formId={formId} target={target} onBusy={setBusy} onClose={onClose} />}
    </Drawer>
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
  const [mode, setMode] = useState<ThemeMode>('light')
  const set = <K extends keyof ThemeForm>(key: K, value: ThemeForm[K]) => setForm((f) => ({ ...f, [key]: value }))
  const setToken = (name: string, value: string) => setForm((f) => ({ ...f, tokens: { ...f.tokens, [mode]: { ...f.tokens[mode], [name]: value } } }))

  const showErrors = (next: ThemeErrors) => {
    setErrors(next)
    const m = firstErrorMode(next)
    if (m && modeErrorCount(next, mode) === 0) setMode(m)
  }

  const save = useMutation({
    mutationFn: (body: object) => api.post('v1/themes', themeSaved, { body, idempotencyKey: intent.keyFor(body) }),
    onMutate: () => onBusy(true),
    onSettled: () => onBusy(false),
    onSuccess: () => {
      intent.reset()
      toast(creating ? `已新建主题「${form.name.trim()}」` : `已保存「${form.name.trim()}」`)
      void invalidate('themes')
      onClose()
    },
    onError: (error) => fail(error, { fields: showErrors, intent }),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildThemeRequest(form, creating)
    if (!built.ok) return showErrors(built.errors)
    setErrors({})
    save.mutate(built.body)
  }

  return (
    <form id={formId} className={css.editor} onSubmit={submit} noValidate>
      <div className={css.fields}>
        <section className={css.block} aria-label="基本信息">
          <div className={css.blockTitle}>基本信息</div>
          <div className={css.pair}>
            <Input label="主题名称" value={form.name} maxLength={60} error={errors.name} onChange={(e) => set('name', e.target.value)} />
            <Input
              label="主题标识（code）"
              mono
              value={form.code}
              readOnly={!creating}
              maxLength={39}
              hint={creating ? '小写字母开头，可含数字、- 与 _；保存后不能再改' : '标识在新建时确定，不能修改'}
              error={errors.code}
              onChange={(e) => set('code', e.target.value)}
            />
          </div>
        </section>

        <section className={css.block} aria-label="站点品牌">
          <div className={css.blockTitle}>站点品牌</div>
          <p className={css.note}>站点名称只有一个来源：生效主题的站点名。门户标题、邮件里的 {'{{site}}'} 与发件人名都取它，切换主题就会一起改变。</p>
          <div className={css.pair}>
            <Input label="站点名称" value={form.siteName} maxLength={40} error={errors['branding.site_name']} onChange={(e) => set('siteName', e.target.value)} />
            <Input label="标语（可选）" value={form.tagline} maxLength={80} error={errors['branding.tagline']} onChange={(e) => set('tagline', e.target.value)} />
          </div>
          <LogoField value={form.logo} error={errors['branding.logo'] ?? errors.branding} onChange={(logo) => set('logo', logo)} />
        </section>

        <section className={css.block} aria-label="配色令牌">
          <div className={css.blockHead}>
            <span className={css.blockTitle}>配色令牌</span>
            <Segmented<ThemeMode>
              label="编辑哪一组令牌"
              size="sm"
              value={mode}
              onChange={setMode}
              options={(['light', 'dark'] as const).map((m) => {
                const n = modeErrorCount(errors, m)
                return { value: m, label: n > 0 ? `${MODE_LABEL[m]}（${n} 处错误）` : MODE_LABEL[m] }
              })}
            />
          </div>
          <p className={css.note}>门户跟随用户的明暗设置取其中一组。只有颜色可以换：字号、间距、圆角属于设计规范，不随主题变化。</p>
          {errors.tokens && <p className={css.error}>{errors.tokens}</p>}
          {TOKEN_SECTIONS.map((section) => (
            <fieldset key={section.group} className={css.section}>
              <legend className={css.sectionTitle}>{section.label}</legend>
              {section.tokens.map((t) => (
                <TokenRow key={t.name} name={t.name} use={t.use} mode={mode} value={form.tokens[mode][t.name] ?? ''} fallback={t[mode]} error={errors[`tokens.${mode}.${t.name}`]} onChange={(v) => setToken(t.name, v)} />
              ))}
            </fieldset>
          ))}
        </section>
      </div>

      <div className={css.previewPane}>
        <div className={css.blockHead}>
          <span className={css.blockTitle}>实时预览</span>
          <span className={css.note}>{MODE_LABEL[mode]} · 只作用于此处</span>
        </div>
        <ThemePreview tokens={form.tokens[mode]} siteName={form.siteName.trim() || 'Pandora'} tagline={form.tagline.trim()} logo={form.logo} />
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// 一个令牌：色块（十六进制时可点开取色器）+ 文本输入
// ---------------------------------------------------------------------------
const HEX = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i
const toHex6 = (v: string) => (v.length === 4 ? `#${[...v.slice(1)].map((c) => c + c).join('')}` : v).toLowerCase()

function TokenRow({ name, use, mode, value, fallback, error, onChange }: { name: string; use: string; mode: ThemeMode; value: string; fallback: string; error?: string; onChange: (value: string) => void }) {
  const id = useId()
  const v = value.trim()
  const chip: CSSProperties = isShadowToken(name) ? { boxShadow: v } : { background: v }
  return (
    <div className={css.token}>
      {HEX.test(v) ? (
        <label className={css.chip} style={chip} title="点击取色">
          <input type="color" className={css.picker} value={toHex6(v)} aria-label={`${name} 取色`} onChange={(e) => onChange(e.target.value)} />
        </label>
      ) : (
        <span className={isShadowToken(name) ? `${css.chip} ${css.chipShadow}` : css.chip} style={chip} aria-hidden="true" />
      )}
      <label className={css.tokenText} htmlFor={id}>
        <span className={css.tokenName}>{name}</span>
        <span className={css.tokenUse}>{use}</span>
      </label>
      <Input id={id} size="sm" mono value={value} placeholder={fallback} aria-label={`${name}（${MODE_LABEL[mode]}）`} error={error} onChange={(e) => onChange(e.target.value)} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Logo：选图片读成 data URL（门户 CSP 只允许 'self' 与 data: 图片）
// ---------------------------------------------------------------------------
function LogoField({ value, error, onChange }: { value: string; error?: string; onChange: (logo: string) => void }) {
  const id = useId()
  const [problem, setProblem] = useState<string | null>(null)
  const pick = (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    const bad = logoProblem(file)
    setProblem(bad)
    if (bad) return
    const reader = new FileReader()
    reader.onload = () => typeof reader.result === 'string' && onChange(reader.result)
    reader.onerror = () => setProblem('读取图片失败，请换一张再试')
    reader.readAsDataURL(file)
  }
  const message = problem ?? error
  return (
    <div className={css.logo}>
      <span className={css.logoLabel}>Logo（可选）</span>
      <div className={css.logoRow}>
        <span className={css.logoBox}>{value ? <img src={value} alt="Logo 预览" /> : <span className={css.note}>未设置</span>}</span>
        <label className={css.upload} htmlFor={id}>
          {value ? '换一张' : '上传图片'}
        </label>
        <input id={id} type="file" className={css.fileInput} accept="image/png,image/jpeg,image/webp,image/svg+xml" onChange={pick} />
        {value && (
          <Button size="xs" variant="link" onClick={() => onChange('')}>
            移除
          </Button>
        )}
        <span className={css.note}>PNG / JPEG / WebP / SVG，48KB 以内；门户不加载外链图片，所以只能上传</span>
      </div>
      {message && (
        <p className={css.error} role="alert">
          {message}
        </p>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 预览：一块缩小的门户 + 后台侧栏，全部颜色经容器上的 CSS 变量取
// ---------------------------------------------------------------------------
function ThemePreview({ tokens, siteName, tagline, logo }: { tokens: Readonly<Record<string, string>>; siteName: string; tagline: string; logo: string }) {
  return (
    <div className={css.preview} style={previewVars(tokens) as CSSProperties} aria-label="主题预览">
      <div className={css.pvHeader}>
        {logo ? <img className={css.pvLogo} src={logo} alt="" /> : <span className={css.pvDot} aria-hidden="true" />}
        <span className={css.pvSite}>{siteName}</span>
        {tagline && <span className={css.pvTagline}>{tagline}</span>}
        <span className={css.pvSpacer} />
        <span className={css.pvNav}>我的订阅</span>
        <span className={css.pvNav}>选购套餐</span>
      </div>
      <div className={css.pvBody}>
        <div className={css.pvBanner}>国庆活动：全场 8 折</div>
        <div className={css.pvCard}>
          <div className={css.pvTitle}>标准版 · 月付</div>
          <div className={css.pvText}>到期 2026-10-31 · 剩余 30 天</div>
          <div className={css.pvMeta}>已用 62.4 GB / 100 GB</div>
          <div className={css.pvTrack}>
            <span className={css.pvFill} />
          </div>
          <div className={css.pvRow}>
            <span className={css.pvPrimary}>续费</span>
            <span className={css.pvSecondary}>复制订阅地址</span>
            <span className={css.pvLink}>使用教程</span>
          </div>
        </div>
        <div className={css.pvRow}>
          <span className={`${css.pvTag} ${css.pvOk}`}>生效中</span>
          <span className={`${css.pvTag} ${css.pvWarn}`}>7 天内到期</span>
          <span className={`${css.pvTag} ${css.pvDanger}`}>欠费</span>
          <span className={`${css.pvTag} ${css.pvInfo}`}>处理中</span>
          <span className={css.pvDangerBtn}>注销账号</span>
        </div>
        <div className={css.pvToast}>
          <span className={css.pvToastDot} aria-hidden="true" />
          已复制订阅地址
        </div>
      </div>
      <div className={css.pvAdmin}>
        <div className={css.pvSidebar}>
          <span className={css.pvSideBrand}>
            <span className={css.pvSideDot} aria-hidden="true" />
            {siteName}
            <span className={css.pvSideMark}>ADMIN</span>
          </span>
          <span className={`${css.pvSideItem} ${css.pvSideActive}`}>内容与外观</span>
          <span className={css.pvSideItem}>营销</span>
        </div>
        <div className={css.pvAdminMain}>
          <span className={css.pvInk}>保存</span>
          <span className={css.pvMeta}>后台侧栏与主按钮</span>
        </div>
      </div>
    </div>
  )
}
