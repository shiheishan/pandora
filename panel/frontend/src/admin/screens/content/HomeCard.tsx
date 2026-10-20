import { Add01Icon, ArrowDown01Icon, ArrowUp01Icon, Delete02Icon } from '@hugeicons-pro/core-stroke-rounded'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useApi } from '../../../shell/runtime'
import { Button, Card, IconButton, Input, QueryView, Skeleton, Switch, TextArea, useToast } from '../../../ui'
import {
  HOME_FAQ_MAX,
  homeChanges,
  moveFaq,
  validateHome,
  type FaqDraft,
} from './homeForm'
import { useFailure, useInvalidateContent } from './queries'
import { homeSettingsSchema, type HomeSettings } from './schemas'
import css from './home.module.css'

function useHomeSettings() {
  const api = useApi()
  return useQuery({
    queryKey: ['admin', 'content', 'home'],
    queryFn: ({ signal }) => api.get('v1/settings/home', homeSettingsSchema, { signal }),
  })
}

function clearFaqFieldErrors(prev: Record<string, string>): Record<string, string> {
  const next = { ...prev }
  for (const k of Object.keys(next)) {
    if (k === 'faq' || k.startsWith('faq.')) delete next[k]
  }
  return next
}

/** 已保存的问答按下标给 key：第一次改动时生成草稿，key 与渲染时一致，输入框不会因此重挂、丢焦点 */
function faqDraftFromCurrent(faq: readonly { q: string; a: string }[]): FaqDraft[] {
  return faq.map((item, i) => ({ key: `faq-saved-${i}`, q: item.q, a: item.a }))
}

export function HomeCard() {
  const api = useApi()
  const toast = useToast()
  const fail = useFailure()
  const invalidate = useInvalidateContent()
  const home = useHomeSettings()
  const keyRef = useRef({ n: 0 })

  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [intro, setIntro] = useState<string | null>(null)
  const [faq, setFaq] = useState<FaqDraft[] | null>(null)
  const [errors, setErrors] = useState<Record<string, string>>({})

  const current = home.data
  const enabledValue = enabled ?? current?.enabled ?? false
  const introValue = intro ?? current?.intro ?? ''
  const faqList: FaqDraft[] = faq ?? faqDraftFromCurrent(current?.faq ?? [])

  const body = current ? homeChanges(current, { enabled, intro, faq }) : {}
  const dirty = Object.keys(body).length > 0

  const save = useMutation({
    mutationFn: (b: Partial<HomeSettings>) => api.post('v1/settings/home', homeSettingsSchema, { body: b }),
    onSuccess: () => {
      toast('首页已保存')
      setEnabled(null)
      setIntro(null)
      setFaq(null)
      setErrors({})
      void invalidate('home')
    },
    onError: (error) => fail(error, { fields: setErrors }),
  })

  const ensureFaqDraft = (): FaqDraft[] => {
    if (faq !== null) return faq
    if (!current) return []
    const next = faqDraftFromCurrent(current.faq)
    setFaq(next)
    return next
  }

  const submit = () => {
    if (!current) return
    const payload = homeChanges(current, { enabled, intro, faq })
    const errs = validateHome(payload)
    if (Object.keys(errs).length > 0) {
      setErrors(errs)
      return
    }
    save.mutate(payload)
  }

  const pending = save.isPending

  return (
    <Card title="首页" subtitle="未登录的访客打开用户端根地址时看到的内容；套餐与价格取自套餐页">
      {home.isPending && <Skeleton height={32} />}
      {home.isError && (
        <QueryView query={home} empty={null}>
          {() => null}
        </QueryView>
      )}
      {current && (
        <div className={css.body}>
          <Switch
            label="在用户端根地址显示首页"
            checked={enabledValue}
            disabled={pending}
            onChange={(e) => {
              setEnabled(e.target.checked)
            }}
          />
          <p className={css.hint}>
            关掉后打开用户端根地址直接是登录页；搜索引擎收录另由上面的「允许搜索引擎收录」决定。
          </p>
          <TextArea
            label="介绍"
            hint="纯文本，换行分段；最多 500 字。留空只显示站点名与标语"
            rows={4}
            maxLength={500}
            value={introValue}
            error={errors.intro}
            disabled={pending}
            onChange={(e) => {
              setIntro(e.target.value)
              if (errors.intro) setErrors((prev) => ({ ...prev, intro: '' }))
            }}
          />
          <div className={css.faqHead}>
            <h3 className={css.faqTitle}>常见问题</h3>
            <span className={css.faqCount}>{faqList.length} / {HOME_FAQ_MAX}</span>
          </div>
          {faqList.length === 0 ? (
            <p className={css.hint}>还没有常见问题。</p>
          ) : (
            <ol className={css.faqList}>
              {faqList.map((item, i) => (
                <li key={item.key} className={css.faqItem}>
                  <div className={css.faqTools}>
                    <IconButton
                      label={`上移第 ${i + 1} 条`}
                      icon={ArrowUp01Icon}
                      size="sm"
                      disabled={pending || i === 0}
                      onClick={() => {
                        setFaq(moveFaq(ensureFaqDraft(), i, -1))
                        setErrors((prev) => clearFaqFieldErrors(prev))
                      }}
                    />
                    <IconButton
                      label={`下移第 ${i + 1} 条`}
                      icon={ArrowDown01Icon}
                      size="sm"
                      disabled={pending || i === faqList.length - 1}
                      onClick={() => {
                        setFaq(moveFaq(ensureFaqDraft(), i, 1))
                        setErrors((prev) => clearFaqFieldErrors(prev))
                      }}
                    />
                    <IconButton
                      label={`删除第 ${i + 1} 条`}
                      icon={Delete02Icon}
                      size="sm"
                      disabled={pending}
                      onClick={() => {
                        const list = ensureFaqDraft()
                        setFaq(list.filter((_, j) => j !== i))
                        setErrors((prev) => clearFaqFieldErrors(prev))
                      }}
                    />
                  </div>
                  <Input
                    label={`问题 ${i + 1}`}
                    maxLength={100}
                    value={item.q}
                    error={errors[`faq.${i}.q`]}
                    disabled={pending}
                    onChange={(e) => {
                      const list = ensureFaqDraft().map((row, j) => (j === i ? { ...row, q: e.target.value } : row))
                      setFaq(list)
                      const key = `faq.${i}.q`
                      if (errors[key]) setErrors((prev) => ({ ...prev, [key]: '' }))
                    }}
                  />
                  <TextArea
                    label="回答"
                    rows={3}
                    maxLength={500}
                    value={item.a}
                    error={errors[`faq.${i}.a`]}
                    disabled={pending}
                    onChange={(e) => {
                      const list = ensureFaqDraft().map((row, j) => (j === i ? { ...row, a: e.target.value } : row))
                      setFaq(list)
                      const key = `faq.${i}.a`
                      if (errors[key]) setErrors((prev) => ({ ...prev, [key]: '' }))
                    }}
                  />
                </li>
              ))}
            </ol>
          )}
          {errors.faq && (
            <p className={css.error} role="alert">
              {errors.faq}
            </p>
          )}
          <div className={css.actions}>
            <Button
              size="sm"
              variant="secondary"
              icon={Add01Icon}
              disabled={pending || faqList.length >= HOME_FAQ_MAX}
              onClick={() => {
                const list = ensureFaqDraft()
                setFaq([...list, { key: `faq-new-${keyRef.current.n++}`, q: '', a: '' }])
                setErrors((prev) => clearFaqFieldErrors(prev))
              }}
            >
              添加一条
            </Button>
            <Button size="sm" disabled={!dirty} busy={pending} onClick={submit}>
              保存
            </Button>
          </div>
        </div>
      )}
    </Card>
  )
}
