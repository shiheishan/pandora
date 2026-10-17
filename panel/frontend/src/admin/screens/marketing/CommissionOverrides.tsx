import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, ConfirmModal, Empty, Input, QueryView, Select, useToast } from '../../../ui'
import { Row, RowList } from '../../kit'
import {
  buildOverrideBody,
  overrideSummary,
  type OverrideForm,
} from './levelsModel'
import local from './CommissionSettings.module.css'
import { formatDateTime, type FieldErrors } from './logic'
import { useCommissionOverrides, useFailure, useInvalidateMarketing } from './queries'
import {
  okResponse,
  overrideSetResponse,
  type CommissionOverride,
} from './schemas'

const emptyForm: OverrideForm = {
  email: '',
  rate: '',
  scope: '',
}

export function CommissionOverrides() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateMarketing()
  const overridesQuery = useCommissionOverrides()

  const [form, setForm] = useState<OverrideForm>(emptyForm)
  const [errors, setErrors] = useState<FieldErrors>({})
  const [deleting, setDeleting] = useState<CommissionOverride | null>(null)

  const saveMutation = useMutation({
    mutationFn: (body: object) =>
      api.post('v1/commission/overrides', overrideSetResponse, { body }),
    onSuccess: () => {
      toast('已保存单独设置')
      setForm(emptyForm)
      setErrors({})
      void invalidate()
    },
    onError: (error) => fail(error, setErrors),
  })

  const submit = (event: FormEvent) => {
    event.preventDefault()
    const built = buildOverrideBody(form)
    if (!built.ok) return setErrors(built.errors)
    setErrors({})
    saveMutation.mutate(built.body)
  }

  const deleteMutation = useMutation({
    mutationFn: (userId: string) =>
      api.delete(`v1/commission/overrides/${userId}`, okResponse),
    onSuccess: () => {
      toast('已删除')
      setDeleting(null)
      void invalidate()
    },
    onError: (error) => {
      fail(error)
      setDeleting(null)
    },
  })

  const editOverride = (o: CommissionOverride) => {
    setForm({
      email: o.email,
      rate: o.rate_percent !== null ? String(o.rate_percent) : '',
      scope: o.scope ?? '',
    })
    setErrors({})
  }

  const hasErrors = Object.keys(errors).length > 0
  const errorMessage = errors.email || errors.rate_percent || errors.scope

  return (
    <Card
      title="单独佣金设置"
      subtitle="给个别推荐人单独的比例或范围"
      flush
      className={local.overridesCard}
    >
      <form onSubmit={submit} noValidate>
        <div className={local.adjf}>
          <Input
            aria-label="用户邮箱"
            placeholder="用户邮箱"
            type="email"
            size="sm"
            value={form.email}
            onChange={(e) => setForm({ ...form, email: e.target.value })}
            className={local.emailInput}
          />
          <Input
            aria-label="佣金比例（%，留空跟全局）"
            placeholder="佣金比例（%，留空跟全局）"
            mono
            inputMode="numeric"
            size="sm"
            value={form.rate}
            onChange={(e) => setForm({ ...form, rate: e.target.value })}
            className={local.rateInput}
          />
          <Select
            aria-label="计佣范围"
            size="sm"
            value={form.scope}
            onChange={(e) =>
              setForm({
                ...form,
                scope: e.target.value as OverrideForm['scope'],
              })
            }
            emptyOption="计佣范围 · 跟全局"
            options={[
              { value: 'first_order', label: '仅首单' },
              { value: 'every_order', label: '每笔订单' },
            ]}
            className={local.scopeSelect}
          />
          <Button type="submit" size="sm" busy={saveMutation.isPending}>
            保存
          </Button>
        </div>
        {hasErrors && (
          <div className={local.errorRow}>
            {errorMessage}
          </div>
        )}
      </form>

      <QueryView
        query={overridesQuery}
        empty={
          <Empty
            bare
            title="还没有单独设置"
            description="不设置时按全局比例与计佣范围计佣。"
          />
        }
      >
        {(rows) => (
          <RowList label="单独佣金设置">
            {rows.map((o) => (
              <Row
                key={o.user_id}
                title={o.email}
                meta={`${overrideSummary(o)} · ${formatDateTime(o.updated_at)}`}
                actions={
                  <>
                    <button
                      type="button"
                      className={local.linkButton}
                      onClick={() => editOverride(o)}
                    >
                      修改
                    </button>
                    <button
                      type="button"
                      className={local.deleteButton}
                      onClick={() => setDeleting(o)}
                      disabled={deleteMutation.isPending}
                    >
                      删除
                    </button>
                  </>
                }
              />
            ))}
          </RowList>
        )}
      </QueryView>

      <ConfirmModal
        open={deleting !== null}
        title="删除单独设置？"
        body={`删除 ${deleting?.email ?? ''} 的单独设置？之后按全局比例与范围计佣。`}
        confirmLabel="删除"
        tone="danger"
        onConfirm={async () => {
          if (deleting) {
            await deleteMutation.mutateAsync(deleting.user_id).catch(() => undefined)
          }
        }}
        onCancel={() => setDeleting(null)}
      />
    </Card>
  )
}
