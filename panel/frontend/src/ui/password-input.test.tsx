import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { PasswordInput } from './index'
import { revealState } from './usePasswordReveal'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('PasswordInput', () => {
  it('revealState returns correct state for false and true', () => {
    expect(revealState(false)).toEqual({ type: 'password', label: '显示密码', pressed: false })
    expect(revealState(true)).toEqual({ type: 'text', label: '隐藏密码', pressed: true })
  })

  it('renders default password input with accessible toggle button and matching label for', () => {
    const out = html(<PasswordInput label="密码" autoComplete="current-password" value="abc123" onChange={noop} />)
    expect(out).toContain('type="password"')
    // HTML 属性名不分大小写，React 19 静态渲染写成 autoComplete
    expect(out.toLowerCase()).toContain('autocomplete="current-password"')
    expect(out).toContain('value="abc123"')
    expect(out).toContain('<button')
    expect(out).toContain('type="button"')
    expect(out).toContain('aria-label="显示密码"')
    expect(out).toContain('aria-pressed="false"')
    expect(out).not.toContain('tabindex="-1"')

    const labelFor = out.match(/<label[^>]*for="([^"]+)"/)?.[1]
    const inputId = out.match(/<input[^>]*id="([^"]+)"/)?.[1]
    expect(labelFor).toBeDefined()
    expect(inputId).toBeDefined()
    expect(labelFor).toBe(inputId)
  })

  it('renders with error wires aria-invalid and aria-describedby to error element id', () => {
    const out = html(<PasswordInput label="密码" error="密码不正确" />)
    expect(out).toContain('aria-invalid="true"')
    const describedBy = out.match(/<input[^>]*aria-describedby="([^"]+)"/)?.[1]
    expect(describedBy).toBeDefined()
    expect(out).toMatch(new RegExp(`id="${describedBy}"[^>]*role="alert"[^>]*>密码不正确<`))
  })

  it('disables both input and toggle button when disabled', () => {
    const out = html(<PasswordInput label="密码" disabled />)
    const buttonMatch = out.match(/<button[^>]*>/)?.[0]
    expect(buttonMatch).toBeDefined()
    expect(buttonMatch).toContain('disabled=""')
  })

  it('preserves autoComplete="new-password"', () => {
    const out = html(<PasswordInput label="密码" autoComplete="new-password" />)
    expect(out.toLowerCase()).toContain('autocomplete="new-password"')
  })
})
