/**
 * [INPUT]: 依赖 react 的 useState / FormEvent，依赖 @tanstack/react-query 的 useMutation，依赖 ../../../core/download 的 saveFile / filenameFromDisposition，依赖 ../../../shell/runtime 的 useApi，依赖 ../../../ui，依赖 ./logic、./queries、./schemas，依赖 ./marketing.module.css 与 ./Gifts.module.css
 * [OUTPUT]: 对外提供 useBatchExport（一次性导出）、GenerateModal（生成一批码）、OneTimeModal（「仅此一次可见」）
 * [POS]: admin/screens/marketing 礼品卡的生码与导出流程（R17）：生码 POST v1/gift-cards/{id}/codes（reauth + 幂等 giftcard_codes_generate）只回前 4 张明文；完整明文只能 POST v1/gift-cards/batches/{id}/export（reauth + 幂等 giftcard_batch_export）一次性导出，经 api.requestRaw 拿 CSV。重放不带 Content-Disposition，文件名按批次 id 前 8 位自拼
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { filenameFromDisposition, saveFile } from '../../../core/download'
import { useApi } from '../../../shell/runtime'
import { Button, Input, Modal, useToast } from '../../../ui'
import local from './Gifts.module.css'
import { batchFileName, batchLabel, buildGenerateRequest, type FieldErrors, type GenerateForm } from './logic'
import css from './marketing.module.css'
import { useFailure, useIntentKey, useInvalidateMarketing } from './queries'
import { codesGenerated, type CodesGenerated, type GiftTemplate } from './schemas'

/** 一次性导出：成功即存文件；409（已导出）等失败也刷新批次，让按钮变成「已导出过」 */
export function useBatchExport(onDone?: () => void) {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const intent = useIntentKey()
  return useMutation({
    mutationFn: async (batchId: string) => {
      const res = await api.requestRaw(`v1/gift-cards/batches/${batchId}/export`, { method: 'POST', body: {}, idempotencyKey: intent.keyFor(batchId) })
      saveFile(await res.blob(), filenameFromDisposition(res.headers.get('Content-Disposition'), batchFileName(batchId)))
    },
    onSuccess: () => {
      intent.reset()
      toast('卡码已导出，文件请妥善保存：完整卡码不会再显示')
      void invalidate()
      onDone?.()
    },
    onError: (error) => {
      fail(error, { intent })
      void invalidate()
    },
  })
}

export function GenerateModal({ template, onClose, onGenerated }: { template: GiftTemplate | null; onClose: () => void; onGenerated: (result: CodesGenerated) => void }) {
  const api = useApi()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const intent = useIntentKey()
  const [form, setForm] = useState<GenerateForm>({ count: '100', prefix: '', expiresAt: '' })
  const [errors, setErrors] = useState<FieldErrors>({})

  const generate = useMutation({
    mutationFn: (body: object) => api.post(`v1/gift-cards/${template!.id}/codes`, codesGenerated, { body, idempotencyKey: intent.keyFor([template!.id, body]) }),
    onSuccess: (result) => {
      intent.reset()
      void invalidate()
      setForm({ count: '100', prefix: '', expiresAt: '' })
      onGenerated(result)
    },
    onError: (error) => fail(error, { fields: setErrors, intent }),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildGenerateRequest(form)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    generate.mutate(built.body)
  }

  return (
    <Modal
      open={template !== null}
      onClose={onClose}
      dismissible={!generate.isPending}
      title={`为「${template?.name ?? ''}」生成一批码`}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={generate.isPending}>
            取消
          </Button>
          <Button size="dialog" variant="primary" type="submit" form="gift-generate" busy={generate.isPending}>
            生成
          </Button>
        </>
      }
    >
      <form id="gift-generate" className={css.stack} onSubmit={submit} noValidate>
        <div className={css.muted}>生成后只显示前 4 张作样例，完整卡码只能一次性导出。</div>
        <Input label="数量（1–5000）" mono inputMode="numeric" value={form.count} onChange={(e) => setForm({ ...form, count: e.target.value })} error={errors.count} data-autofocus="" />
        <Input label="前缀（可选）" mono maxLength={8} value={form.prefix} onChange={(e) => setForm({ ...form, prefix: e.target.value.toUpperCase() })} error={errors.prefix} placeholder="大写字母和数字，最多 8 位" />
        <Input label="有效期至（可选）" type="datetime-local" value={form.expiresAt} onChange={(e) => setForm({ ...form, expiresAt: e.target.value })} error={errors.expires_at} hint="留空则长期有效" />
      </form>
    </Modal>
  )
}

/** 设计稿 oneTime：显示 sample 加「…」；「导出 CSV」直接一次性导出，「已保存，关闭」只关弹窗，批次保持未导出 */
export function OneTimeModal({ result, onClose }: { result: CodesGenerated | null; onClose: () => void }) {
  const exporter = useBatchExport(onClose)
  return (
    <Modal
      open={result !== null}
      onClose={onClose}
      dismissible={!exporter.isPending}
      eyebrow={<span className={local.eyebrow}>仅此一次可见</span>}
      title={result ? `批次 ${batchLabel(result.batch)} 已生成 ${result.count} 张` : ''}
      actions={
        <>
          <Button size="dialog" onClick={onClose} disabled={exporter.isPending}>
            已保存，关闭
          </Button>
          <Button size="dialog" variant="primary" busy={exporter.isPending} onClick={() => result && exporter.mutate(result.batch_id)} data-autofocus="">
            导出 CSV
          </Button>
        </>
      }
    >
      {result && (
        <div className={css.stack}>
          <div className={css.muted}>关闭后完整卡码不再显示，只能看到掩码；批次在导出前还能从「批次与卡码」里一次性导出。</div>
          <div className={local.sample}>
            {result.sample.map((code) => (
              <span key={code}>{code}</span>
            ))}
            {result.count > result.sample.length && <span className={css.faint}>…</span>}
          </div>
        </div>
      )}
    </Modal>
  )
}
