/**
 * 人机验证：按需加载 Google reCAPTCHA 或 Cloudflare Turnstile 外链脚本。
 * 入口页 CSP 的 script-src、frame-src 须放行对应来源（google.com / gstatic.com 或 challenges.cloudflare.com），
 * 由后端 panel/internal/pkg/webapp 按设置下发，前端不改 CSP。
 */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { SignupConfig } from './queries'
import css from './Captcha.module.css'

export type CaptchaConfig = NonNullable<SignupConfig['captcha']>

interface GrecaptchaApi {
  ready(cb: () => void): void
  render(el: HTMLElement, o: Record<string, unknown>): number
  reset(id?: number): void
  execute(key: string, o: { action: string }): Promise<string>
}

interface TurnstileApi {
  render(el: HTMLElement, o: Record<string, unknown>): string
  reset(id?: string): void
  remove(id: string): void
}

function win(): { grecaptcha?: GrecaptchaApi; turnstile?: TurnstileApi } {
  return window as unknown as { grecaptcha?: GrecaptchaApi; turnstile?: TurnstileApi }
}

const scriptLoads = new Map<string, Promise<void>>()

function loadScript(src: string): Promise<void> {
  const cached = scriptLoads.get(src)
  if (cached) return cached

  const promise = new Promise<void>((resolve, reject) => {
    const existing = document.querySelector(`script[src="${src}"]`)
    if (existing) {
      existing.addEventListener('load', () => resolve(), { once: true })
      existing.addEventListener('error', () => reject(new Error('load failed')), { once: true })
      return
    }
    const el = document.createElement('script')
    el.src = src
    el.async = true
    el.defer = true
    el.onload = () => resolve()
    el.onerror = () => {
      scriptLoads.delete(src)
      reject(new Error('load failed'))
    }
    document.head.appendChild(el)
  })
  scriptLoads.set(src, promise)
  return promise
}

export function captchaScriptUrl(c: CaptchaConfig): string {
  switch (c.provider) {
    case 'recaptcha_v2':
      return 'https://www.google.com/recaptcha/api.js?render=explicit'
    case 'recaptcha_v3':
      return `https://www.google.com/recaptcha/api.js?render=${encodeURIComponent(c.site_key)}`
    case 'turnstile':
      return 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
  }
}

const LOAD_FAIL = '人机验证加载失败，请检查网络后刷新页面'
const NEED_COMPLETE = '请先完成人机验证'

export function useCaptcha(config: CaptchaConfig | null): {
  element: ReactNode
  getToken: (action: string) => Promise<string>
  reset: () => void
} {
  const tokenRef = useRef('')
  const widgetIdRef = useRef<number | string | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [loadFailed, setLoadFailed] = useState(false)

  const clearToken = useCallback(() => {
    tokenRef.current = ''
  }, [])

  const reset = useCallback(() => {
    if (!config) return
    if (config.provider === 'recaptcha_v3') return
    const w = win()
    if (config.provider === 'turnstile') {
      const id = widgetIdRef.current
      if (typeof id === 'string') w.turnstile?.reset(id)
    } else {
      const id = widgetIdRef.current
      if (typeof id === 'number') w.grecaptcha?.reset(id)
    }
    clearToken()
  }, [config, clearToken])

  useEffect(() => {
    if (!config || config.provider === 'recaptcha_v3') return

    let cancelled = false
    const src = captchaScriptUrl(config)
    setLoadFailed(false)
    tokenRef.current = ''
    widgetIdRef.current = null

    void loadScript(src)
      .then(() => {
        if (cancelled) return
        const el = containerRef.current
        if (!el) return

        if (config.provider === 'turnstile') {
          const turnstile = win().turnstile
          if (!turnstile) {
            setLoadFailed(true)
            return
          }
          const id = turnstile.render(el, {
            sitekey: config.site_key,
            callback: (t: string) => {
              tokenRef.current = t
            },
            'expired-callback': clearToken,
            'error-callback': clearToken,
          })
          widgetIdRef.current = id
          return
        }

        const grecaptcha = win().grecaptcha
        if (!grecaptcha) {
          setLoadFailed(true)
          return
        }
        grecaptcha.ready(() => {
          if (cancelled) return
          const host = containerRef.current
          if (!host) return
          const id = grecaptcha.render(host, {
            sitekey: config.site_key,
            callback: (t: string) => {
              tokenRef.current = t
            },
            'expired-callback': clearToken,
            'error-callback': clearToken,
          })
          widgetIdRef.current = id
        })
      })
      .catch(() => {
        if (!cancelled) setLoadFailed(true)
      })

    return () => {
      cancelled = true
      if (config.provider === 'turnstile') {
        const id = widgetIdRef.current
        if (typeof id === 'string') win().turnstile?.remove(id)
      }
      widgetIdRef.current = null
      tokenRef.current = ''
    }
  }, [config, clearToken])

  const getToken = useCallback(
    async (action: string): Promise<string> => {
      if (!config) return ''
      if (loadFailed) throw new Error(LOAD_FAIL)

      if (config.provider === 'recaptcha_v3') {
        const src = captchaScriptUrl(config)
        try {
          await loadScript(src)
        } catch {
          throw new Error(LOAD_FAIL)
        }
        const grecaptcha = win().grecaptcha
        if (!grecaptcha) throw new Error(LOAD_FAIL)
        return new Promise((resolve, reject) => {
          grecaptcha.ready(() => {
            void grecaptcha.execute(config.site_key, { action }).then(resolve).catch(() => reject(new Error(LOAD_FAIL)))
          })
        })
      }

      const t = tokenRef.current
      if (!t) throw new Error(NEED_COMPLETE)
      return t
    },
    [config, loadFailed],
  )

  const element = useMemo((): ReactNode => {
    if (!config) return null
    if (config.provider === 'recaptcha_v3') {
      return <p className={css.note}>本站使用 reCAPTCHA 保护</p>
    }
    return (
      <div className={css.box}>
        {loadFailed ? (
          <p className={css.loadFail}>{LOAD_FAIL}</p>
        ) : (
          <div ref={containerRef} />
        )}
      </div>
    )
  }, [config, loadFailed])

  return { element, getToken, reset }
}
