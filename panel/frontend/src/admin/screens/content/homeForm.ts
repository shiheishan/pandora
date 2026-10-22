import type { HomeSettings } from './schemas'

/** 与后端 settings/service/home.go 的上限一致 */
export const HOME_INTRO_MAX = 500
export const HOME_FAQ_MAX = 20
export const HOME_Q_MAX = 100
export const HOME_A_MAX = 500

/** 编辑中的一条问答；key 只给 React 列表用，不提交 */
export interface FaqDraft {
  key: string
  q: string
  a: string
}

/** 同后端：\r\n 与 \r 换成 \n，再去首尾空白 */
export function normalizeMultiline(s: string): string {
  return s.replace(/\r\n/g, '\n').replace(/\r/g, '\n').trim()
}

/** 把编辑中的问答规整成提交形状（q 去首尾空白，a 用 normalizeMultiline） */
export function normalizeFaq(list: readonly FaqDraft[]): HomeSettings['faq'] {
  return list.map((item) => ({
    q: item.q.trim(),
    a: normalizeMultiline(item.a),
  }))
}

function faqEqual(a: HomeSettings['faq'], b: HomeSettings['faq']): boolean {
  return a.length === b.length && a.every((x, i) => x.q === b[i]?.q && x.a === b[i]?.a)
}

/** 只放改了的键；都没改时返回空对象。draft 里的 null 表示没动过 */
export function homeChanges(
  current: HomeSettings,
  draft: { enabled: boolean | null; intro: string | null; faq: readonly FaqDraft[] | null },
): Partial<HomeSettings> {
  const body: Partial<HomeSettings> = {}
  if (draft.enabled !== null && draft.enabled !== current.enabled) {
    body.enabled = draft.enabled
  }
  if (draft.intro !== null) {
    const intro = normalizeMultiline(draft.intro)
    if (intro !== current.intro) body.intro = intro
  }
  if (draft.faq !== null) {
    const faq = normalizeFaq(draft.faq)
    if (!faqEqual(faq, current.faq)) body.faq = faq
  }
  return body
}

// eslint-disable-next-line no-control-regex
const INTRO_CONTROL = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/
// eslint-disable-next-line no-control-regex
const Q_CONTROL = /[\u0000-\u001f\u007f-\u009f]/
// eslint-disable-next-line no-control-regex
const A_CONTROL = /[\u0000-\u0009\u000b-\u001f\u007f-\u009f]/

/** 前端先校验，文案与键和后端一字不差；返回 {} 表示通过 */
export function validateHome(body: Partial<HomeSettings>): Record<string, string> {
  const errs: Record<string, string> = {}
  if (body.intro !== undefined) {
    const s = body.intro
    if ([...s].length > HOME_INTRO_MAX) {
      errs.intro = '介绍最多 500 个字'
    } else if (INTRO_CONTROL.test(s)) {
      errs.intro = '介绍不能包含控制字符'
    }
  }
  if (body.faq !== undefined) {
    const list = body.faq
    if (list.length > HOME_FAQ_MAX) {
      errs.faq = '常见问题最多 20 条'
    } else {
      for (const [i, { q, a }] of list.entries()) {
        const qKey = `faq.${i}.q`
        const aKey = `faq.${i}.a`
        if (!q) {
          errs[qKey] = '问题必填'
        } else if ([...q].length > HOME_Q_MAX) {
          errs[qKey] = '问题最多 100 个字'
        } else if (Q_CONTROL.test(q)) {
          errs[qKey] = '问题不能包含换行或控制字符'
        }
        if (!a) {
          errs[aKey] = '回答必填'
        } else if ([...a].length > HOME_A_MAX) {
          errs[aKey] = '回答最多 500 个字'
        } else if (A_CONTROL.test(a)) {
          errs[aKey] = '回答不能包含控制字符'
        }
      }
    }
  }
  return errs
}

/** 上移 / 下移一条（越界原样返回新数组） */
export function moveFaq<T>(list: readonly T[], index: number, delta: -1 | 1): T[] {
  const next = index + delta
  if (index < 0 || index >= list.length || next < 0 || next >= list.length) {
    return [...list]
  }
  const out = [...list]
  out.splice(next, 0, ...out.splice(index, 1))
  return out
}
