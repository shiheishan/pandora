/**
 * [INPUT]: 依赖 react 的 useEffect / useRef / useState，依赖 @tanstack/react-query 的 useMutation / useQuery / useQueryClient，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic 的模板函数，依赖 ./queries，依赖 ./schemas，依赖 ./system.module.css
 * [OUTPUT]: 对外提供 TemplatesTab（通知与插件 · 邮件模板标签）
 * [POS]: admin/screens/system 的邮件模板（设计稿 t_templates）：左栏渠道分段（邮件 / 站内信 / Telegram，契约待补·前端）+ 模板列表（名称、默认 / 已自定义 / 未保存的修改 / 无内置默认），中栏主题、正文与变量 chips（插入 {{name}}，取自 allowed_variables），右栏「以示例数据预览」。
 *        预览：没改时用列表带回的 preview_*，改了 300ms 防抖后 POST v1/mail/templates/preview（示例值只在后端一份），白名单外的变量当场提示；保存与恢复默认要 platform.settings.write（无 reauth、无幂等），恢复默认先确认、没有内置默认或已是默认时禁用；
 *        实发测试信只对邮件渠道显示（ops.notification.write + reauth），有未保存修改时直接发草稿。切换模板时各自的草稿都留着。设计里的「重置密码」「礼品卡兑换成功」后端没有（D-A-5 已决：不做），按后端现有模板显示
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, ConfirmModal, Empty, Input, QueryView, Segmented, TextArea, useToast } from '../../../ui'
import { CHANNEL_LABEL, insertVariable, templateKey, templateMeta, templateName, validateTemplate, type Draft } from './logic'
import { SK, useCan, useFailure, useTemplates } from './queries'
import { CHANNELS, templatePreview, templateReset, templateSaved, templateTested, type Channel, type Template, type TemplateRow } from './schemas'
import css from './system.module.css'

export function TemplatesTab() {
  const templates = useTemplates()
  const [channel, setChannel] = useState<Channel>('email')
  const [selected, setSelected] = useState<string | null>(null)
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})

  return (
    <QueryView query={templates} rows={6} empty={<Empty bare title="没有通知模板" description="模板由系统迁移写入，升级后会出现在这里。" />}>
      {(list) => {
        const inChannel = list.filter((t) => t.channel === channel)
        const current = inChannel.find((t) => templateKey(t) === selected) ?? inChannel[0] ?? null
        return (
          <div className={css.tplLayout}>
            <nav className={css.tplList} aria-label="模板列表">
              <div className={css.tplTools}>
                <Segmented size="sm" label="渠道" value={channel} options={CHANNELS.map((c) => ({ value: c, label: CHANNEL_LABEL[c] }))} onChange={setChannel} />
              </div>
              {inChannel.map((t) => {
                const key = templateKey(t)
                const meta = templateMeta(t, drafts[key])
                const on = current !== null && templateKey(current) === key
                return (
                  <button key={key} type="button" className={`${css.tplItem} ${on ? css.tplCurrent : ''}`} aria-current={on || undefined} onClick={() => setSelected(key)}>
                    <span className={css.tplName}>{templateName(t.code)}</span>
                    <span className={`${css.tplMeta} ${css[meta.tone]}`}>{meta.label}</span>
                  </button>
                )
              })}
              {inChannel.length === 0 && (
                <div className={css.pad}>
                  <Empty bare title={`没有${CHANNEL_LABEL[channel]}模板`} description="这个渠道目前没有可编辑的模板。" />
                </div>
              )}
            </nav>
            {current ? (
              <TemplateEditor
                key={templateKey(current)}
                template={current}
                draft={drafts[templateKey(current)]}
                onDraft={(d) => setDrafts((all) => {
                  const next = { ...all }
                  if (d) next[templateKey(current)] = d
                  else delete next[templateKey(current)]
                  return next
                })}
              />
            ) : (
              <section className={`${css.panel} ${css.spanRest}`}>
                <Empty bare title="没有选中的模板" description="左侧选一个模板。" />
              </section>
            )}
          </div>
        )
      }}
    </QueryView>
  )
}

/** 值稳定 delay 毫秒后才更新，给预览请求防抖 */
function useDebounced<T>(value: T, delay: number): T {
  const [out, setOut] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setOut(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return out
}

function TemplateEditor({ template: t, draft, onDraft }: { template: Template; draft: Draft | undefined; onDraft: (d: Draft | null) => void }) {
  const api = useApi()
  const can = useCan()
  const toast = useToast()
  const fail = useFailure()
  const client = useQueryClient()
  const body = useRef<HTMLTextAreaElement>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [resetting, setResetting] = useState(false)
  const [to, setTo] = useState('')
  const value: Draft = draft ?? { subject: t.subject, body: t.body }
  const dirty = value.subject !== t.subject || value.body !== t.body
  const writable = can('platform.settings.write')
  const edit = (patch: Partial<Draft>) => {
    setErrors({})
    const next = { ...value, ...patch }
    onDraft(next.subject === t.subject && next.body === t.body ? null : next)
  }

  // ---- 预览：没改用列表带回的，改了防抖后请后端渲染 ----
  // 只防抖字符串：对象每次渲染都是新的，拿它当依赖会 300ms 一轮地自己重渲染
  const subject = useDebounced(value.subject, 300)
  const text = useDebounced(value.body, 300)
  const preview = useQuery({
    queryKey: [...SK, 'preview', t.code, t.channel, subject, text],
    queryFn: ({ signal }) => api.post('v1/mail/templates/preview', templatePreview, { signal, body: { code: t.code, channel: t.channel, subject, body: text } }),
    enabled: dirty,
    placeholderData: (prev) => prev,
    staleTime: Infinity,
  })
  const shown = dirty && preview.data ? preview.data : { preview_subject: t.preview_subject, preview_body: t.preview_body, unknown_variables: [] }

  /** 保存 / 恢复后把新行写回列表缓存（响应不含 has_default / description，沿用原值），再重拉一次 */
  const writeBack = (row: TemplateRow, rendered?: { preview_subject: string; preview_body: string }) => {
    client.setQueryData<Template[]>([...SK, 'templates'], (list) => list?.map((x) => (templateKey(x) === templateKey(row) ? { ...x, ...row, ...rendered } : x)))
    onDraft(null)
    void client.invalidateQueries({ queryKey: [...SK, 'templates'] })
  }

  const save = useMutation({
    mutationFn: (d: Draft) => api.post('v1/mail/templates', templateSaved, { body: { code: t.code, channel: t.channel, subject: d.subject, body: d.body } }),
    onSuccess: (r) => {
      toast('模板已保存')
      writeBack(r.template, { preview_subject: r.preview_subject, preview_body: r.preview_body })
    },
    onError: (e) => fail(e, (f) => setErrors(f)),
  })
  const reset = useMutation({
    mutationFn: () => api.post('v1/mail/templates/reset', templateReset, { body: { code: t.code, channel: t.channel } }),
    onSuccess: (r) => {
      toast('已恢复默认')
      setErrors({})
      // 恢复的响应不带渲染结果，预览等列表重拉
      writeBack(r.template)
    },
    onError: (e) => fail(e),
  })
  const test = useMutation({
    mutationFn: (address: string) => api.post('v1/mail/templates/test', templateTested, { body: { code: t.code, channel: t.channel, to: address, ...(dirty ? { subject: value.subject, body: value.body } : {}) } }),
    onSuccess: () => toast(`测试信已发送至 ${to.trim()}`),
    onError: (e) => fail(e, (f) => setErrors(f)),
  })

  const submit = () => {
    const errs = validateTemplate(value)
    setErrors(errs)
    if (Object.keys(errs).length === 0) save.mutate(value)
  }

  const insert = (name: string) => {
    const el = body.current
    const next = insertVariable(value.body, el?.selectionStart ?? value.body.length, el?.selectionEnd ?? value.body.length, name)
    edit({ body: next.text })
    requestAnimationFrame(() => {
      el?.focus()
      el?.setSelectionRange(next.cursor, next.cursor)
    })
  }

  const resetBlock = !t.has_default ? '这个模板没有内置默认内容' : t.is_default && !dirty ? '已经是默认内容' : null
  const busy = save.isPending || reset.isPending
  return (
    <>
      <section className={css.panel} aria-label={`编辑模板 ${templateName(t.code)}`}>
        <div className={css.tplHead}>
          <span className={css.cardTitle}>{templateName(t.code)}</span>
          <span className={`${css.mono} ${css.faint}`}>
            {t.code} · {CHANNEL_LABEL[t.channel]}
          </span>
        </div>
        {t.description && <div className={css.faint}>{t.description}</div>}
        <Input label="主题" value={value.subject} error={errors.subject} readOnly={!writable} disabled={busy} onChange={(e) => edit({ subject: e.target.value })} />
        <TextArea ref={body} label="正文" mono rows={11} value={value.body} error={errors.body} readOnly={!writable} disabled={busy} onChange={(e) => edit({ body: e.target.value })} />
        {writable && t.allowed_variables.length > 0 && (
          <div className={css.chips} aria-label="插入变量">
            {t.allowed_variables.map((v) => (
              <button key={v} type="button" className={css.chip} onClick={() => insert(v)}>
                {`{{${v}}}`}
              </button>
            ))}
          </div>
        )}
        {shown.unknown_variables.length > 0 && !errors.body && (
          <div className={css.error} role="alert">
            用到了这个模板不提供的变量：{shown.unknown_variables.join('、')}，保存会被拒绝。可用变量：{t.allowed_variables.join('、') || '无'}
          </div>
        )}
        {writable && (
          <div className={css.row}>
            <Button size="sm" disabled={busy || resetBlock !== null} title={resetBlock ?? undefined} busy={reset.isPending} onClick={() => setResetting(true)}>
              恢复默认
            </Button>
            {dirty && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => onDraft(null)}>
                放弃修改
              </Button>
            )}
            <span className={css.spacer} />
            <Button size="sm" variant="primary" disabled={!dirty} busy={save.isPending} onClick={submit}>
              保存
            </Button>
          </div>
        )}
      </section>

      <section className={css.previewCard} aria-label="示例数据预览">
        <div className={css.previewHead}>
          <span className={css.faint}>以示例数据预览</span>
          {dirty && preview.isFetching && <span className={css.faint}>渲染中…</span>}
        </div>
        <div className={css.previewBody}>
          <div className={css.previewSubject}>{shown.preview_subject}</div>
          <div className={css.previewText}>{shown.preview_body}</div>
        </div>
        {t.channel === 'email' && can('ops.notification.write') && (
          <div className={css.cardFoot}>
            <Input size="sm" type="email" aria-label="测试收件地址" fieldClassName={css.grow} placeholder="收件地址" value={to} error={errors.to} onChange={(e) => setTo(e.target.value)} />
            <Button size="sm" disabled={!to.trim()} busy={test.isPending} title={dirty ? '发送的是当前未保存的草稿' : undefined} onClick={() => test.mutate(to.trim())}>
              实发测试信
            </Button>
          </div>
        )}
      </section>

      <ConfirmModal
        open={resetting}
        title={`恢复「${templateName(t.code)}」的默认内容？`}
        body="自定义的主题与正文会被内置默认内容覆盖，不能撤销；未保存的修改也会一并丢弃。"
        confirmLabel="恢复默认"
        tone="danger"
        onConfirm={() => {
          reset.mutate()
          setResetting(false)
        }}
        onCancel={() => setResetting(false)}
      />
    </>
  )
}
