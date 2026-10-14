import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { isApiError } from '../../../core/api'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, QueryView, TextArea, useToast } from '../../../ui'
import {
  formatLabel,
  formatSize,
  missingPlaceholders,
  TEMPLATE_FORMATS,
  templateBytes,
  templateDetailSchema,
  templatePreviewSchema,
  type TemplateDetail,
  type TemplateSummary,
} from './subscription'
import {
  SUB_KEY,
  useFailure,
  useInvalidateSubscription,
  useSubscriptionTemplate,
  useSubscriptionTemplates,
} from './subscriptionQueries'
import css from './subscription.module.css'

/** 在光标处插入 token，返回新文本与插入后的光标位置 */
function insertToken(text: string, start: number, end: number, token: string): { text: string; cursor: number } {
  const a = Math.max(0, Math.min(start, text.length))
  const b = Math.max(a, Math.min(end, text.length))
  return { text: text.slice(0, a) + token + text.slice(b), cursor: a + token.length }
}

function useDebounced<T>(value: T, delay: number): T {
  const [out, setOut] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setOut(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return out
}

function listMeta(row: TemplateSummary, draft: string | undefined): { label: string; tone: 'accent' | 'faint' | 'warn' } {
  if (draft !== undefined) return { label: '未保存', tone: 'warn' }
  if (row.customized) return { label: `已自定义 · ${formatSize(row.bytes)}`, tone: 'accent' }
  return { label: '内置', tone: 'faint' }
}

export function SubscriptionTemplates() {
  const templates = useSubscriptionTemplates()
  const [selected, setSelected] = useState<string | null>(null)
  const [drafts, setDrafts] = useState<Record<string, string>>({})

  return (
    <Card title="订阅模板">
      <p className={css.intro}>
        每种格式一份。占位符会在拉取时换成这份订阅的节点；没改过的格式用内置模板。保存后最多 30 秒在订阅里生效。Shadowrocket、V2RayN
        一类的分享链接订阅和 SIP008 没有模板。
      </p>
      <QueryView query={templates} rows={6} empty={<Empty bare title="没有订阅模板" description="模板列表为空。" />}>
        {(list) => {
          const formats = TEMPLATE_FORMATS.filter((f) => list.some((r) => r.format === f))
          const rows = formats.map((f) => list.find((r) => r.format === f)!)
          const currentFormat = selected ?? formats[0] ?? null
          return (
            <div className={css.tplLayout}>
              <nav className={css.tplList} aria-label="订阅格式">
                {rows.map((row) => {
                  const on = row.format === currentFormat
                  const meta = listMeta(row, drafts[row.format])
                  return (
                    <button
                      key={row.format}
                      type="button"
                      className={`${css.tplItem} ${on ? css.tplCurrent : ''}`}
                      aria-current={on || undefined}
                      onClick={() => setSelected(row.format)}
                    >
                      <span className={css.tplName}>{formatLabel(row.format)}</span>
                      <span className={`${css.tplMeta} ${css.tplMetaMono} ${css[meta.tone]}`}>{meta.label}</span>
                    </button>
                  )
                })}
              </nav>
              {currentFormat ? (
                <TemplateEditor
                  key={currentFormat}
                  format={currentFormat}
                  draft={drafts[currentFormat]}
                  onDraft={(d) =>
                    setDrafts((all) => {
                      const next = { ...all }
                      if (d === null) delete next[currentFormat]
                      else next[currentFormat] = d
                      return next
                    })
                  }
                />
              ) : (
                <section className={`${css.panel} ${css.spanRest}`}>
                  <Empty bare title="没有选中的格式" description="左侧选一个订阅格式。" />
                </section>
              )}
            </div>
          )
        }}
      </QueryView>
    </Card>
  )
}

function TemplateEditor({
  format,
  draft,
  onDraft,
}: {
  format: string
  draft: string | undefined
  onDraft: (content: string | null) => void
}) {
  const detailQuery = useSubscriptionTemplate(format)

  return (
    <QueryView query={detailQuery} rows={8} empty={null}>
      {(detail) => (
        <TemplateEditorBody key={format} format={format} detail={detail} draft={draft} onDraft={onDraft} />
      )}
    </QueryView>
  )
}

function TemplateEditorBody({
  format,
  detail,
  draft,
  onDraft,
}: {
  format: string
  detail: TemplateDetail
  draft: string | undefined
  onDraft: (content: string | null) => void
}) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateSubscription()
  const client = useQueryClient()
  const body = useRef<HTMLTextAreaElement>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [resetting, setResetting] = useState(false)

  const value = draft ?? detail.content
  const dirty = draft !== undefined && draft !== detail.content

  const missing = missingPlaceholders(value, detail.placeholders)
  const overLimit = templateBytes(value) > detail.max_bytes
  const validationBlock = missing.length > 0 || overLimit

  const missingMsg = missing.length > 0 ? `缺少必需占位：${missing.join('、')}` : null
  const limitMsg = overLimit ? `超过 ${formatSize(detail.max_bytes)} 上限` : null
  const inlineError = errors.content ?? missingMsg ?? limitMsg ?? undefined

  const edit = (next: string) => {
    setErrors({})
    onDraft(next === detail.content ? null : next)
  }

  const debouncedContent = useDebounced(value, 400)
  const previewEnabled = !validationBlock
  const preview = useQuery({
    queryKey: [...SUB_KEY, 'preview', format, debouncedContent],
    queryFn: ({ signal }) =>
      api.post(`v1/subscription-templates/${encodeURIComponent(format)}/preview`, templatePreviewSchema, {
        signal,
        body: { content: debouncedContent },
      }),
    enabled: previewEnabled,
    placeholderData: (prev) => prev,
  })

  const writeBack = (row: TemplateDetail) => {
    client.setQueryData([...SUB_KEY, 'template', format], row)
    client.setQueryData<TemplateSummary[]>([...SUB_KEY, 'templates'], (list) =>
      list?.map((x) =>
        x.format === format
          ? { ...x, customized: row.customized, bytes: templateBytes(row.content), updated_at: row.updated_at }
          : x,
      ),
    )
    onDraft(null)
    void invalidate()
  }

  const save = useMutation({
    mutationFn: (content: string) =>
      api.put(`v1/subscription-templates/${encodeURIComponent(format)}`, templateDetailSchema, { body: { content } }),
    onSuccess: (r) => {
      toast('模板已保存，最多 30 秒后在订阅里生效')
      writeBack(r)
    },
    onError: (e) => fail(e, setErrors),
  })

  const reset = useMutation({
    mutationFn: () => api.delete(`v1/subscription-templates/${encodeURIComponent(format)}`, templateDetailSchema),
    onSuccess: (r) => {
      toast('已恢复内置模板')
      setErrors({})
      writeBack(r)
    },
    onError: (e) => fail(e),
  })

  const insert = (token: string) => {
    const el = body.current
    const next = insertToken(value, el?.selectionStart ?? value.length, el?.selectionEnd ?? value.length, token)
    edit(next.text)
    requestAnimationFrame(() => {
      el?.focus()
      el?.setSelectionRange(next.cursor, next.cursor)
    })
  }

  const resetBlock = !detail.customized && !dirty ? '已经是内置模板' : null
  const busy = save.isPending || reset.isPending

  return (
    <div className={css.rightCol}>
      <section className={css.panel} aria-label={`编辑 ${formatLabel(format)} 模板`}>
        <div className={css.tplHead}>
          <span className={css.cardTitle}>{formatLabel(format)}</span>
          <span className={`${css.mono} ${css.faint}`}>{format}</span>
        </div>
        <TextArea
          ref={body}
          label="模板内容"
          mono
          rows={18}
          value={value}
          error={inlineError}
          disabled={busy}
          onChange={(e) => edit(e.target.value)}
        />
        {detail.placeholders.length > 0 && (
          <div className={css.chips} aria-label="插入占位符">
            {detail.placeholders.map((p) => (
              <button
                key={p.token}
                type="button"
                className={css.chip}
                title={p.required ? `${p.desc}（必需）` : p.desc}
                onClick={() => insert(p.token)}
              >
                {p.token}{p.required ? '（必需）' : ''}
              </button>
            ))}
          </div>
        )}
        {(missingMsg || limitMsg) && !errors.content && (
          <div className={css.error} role="alert">
            {[missingMsg, limitMsg].filter(Boolean).join('；')}
          </div>
        )}
        <div className={css.row}>
          <Button size="sm" disabled={busy || resetBlock !== null} title={resetBlock ?? undefined} busy={reset.isPending} onClick={() => setResetting(true)}>
            恢复内置
          </Button>
          {dirty && (
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => onDraft(null)}>
              放弃修改
            </Button>
          )}
          <span className={css.spacer} />
          <Button
            size="sm"
            variant="primary"
            disabled={!dirty || validationBlock || busy}
            busy={save.isPending}
            onClick={() => save.mutate(value)}
          >
            保存
          </Button>
        </div>
      </section>

      <section className={css.previewCard} aria-label="示例节点预览">
        <div className={css.previewHead}>
          <span className={css.faint}>以示例节点预览</span>
          {previewEnabled && preview.isFetching && <span className={css.faint}>渲染中…</span>}
        </div>
        <div className={css.previewBody}>
          {validationBlock ? (
            <span className={css.faint}>修正模板中的错误后可预览</span>
          ) : preview.isError && isApiError(preview.error) ? (
            <div className={css.error} role="alert">{preview.error.message}</div>
          ) : preview.data ? (
            <>
              <span className={css.faint}>{preview.data.node_count} 条示例节点 · {preview.data.content_type}</span>
              <pre className={css.previewText}>{preview.data.body}</pre>
            </>
          ) : (
            <span className={css.faint}>加载预览…</span>
          )}
        </div>
      </section>

      <ConfirmModal
        open={resetting}
        title={`恢复「${formatLabel(format)}」的内置模板？`}
        body="自定义内容会被内置模板覆盖，不能撤销；未保存的修改也会一并丢弃。"
        confirmLabel="恢复内置"
        tone="danger"
        onConfirm={() => {
          reset.mutate()
          setResetting(false)
        }}
        onCancel={() => setResetting(false)}
      />
    </div>
  )
}
