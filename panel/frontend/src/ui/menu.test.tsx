import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Menu } from './index'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('Menu', () => {
  it('renders entry icon inside role="menuitem" button when open', () => {
    const out = html(
      <Menu
        open
        label="账户"
        trigger="张"
        triggerLabel="账户菜单"
        entries={[
          {
            key: 'pwd',
            label: '修改密码',
            icon: <svg data-x="1" />,
            onSelect: noop,
          },
        ]}
      />,
    )
    expect(out).toContain('data-x="1"')
    const item = /<button[^>]*role="menuitem"[^>]*>[\s\S]*?<\/button>/.exec(out)?.[0]
    expect(item).toBeTruthy()
    expect(item).toContain('data-x="1"')
  })

  it('renders choice row with role="group" and role="menuitemradio" options', () => {
    const out = html(
      <Menu
        open
        label="账户"
        trigger="张"
        triggerLabel="账户菜单"
        entries={[
          {
            kind: 'choice',
            key: 'appearance',
            label: '外观',
            options: [
              { value: 'light', label: '浅色' },
              { value: 'dark', label: '深色' },
            ],
            value: 'dark',
            onChange: noop,
          },
        ]}
      />,
    )
    expect(out).toContain('role="group"')
    expect(out).toContain('aria-label="外观"')
    const radios = out.match(/role="menuitemradio"/g) ?? []
    expect(radios).toHaveLength(2)

    const dark = /<button[^>]*>深色<\/button>/.exec(out)?.[0]
    expect(dark).toBeTruthy()
    expect(dark).toContain('aria-checked="true"')

    const light = /<button[^>]*>浅色<\/button>/.exec(out)?.[0]
    expect(light).toBeTruthy()
    expect(light).toContain('aria-checked="false"')
  })

  it('placement="top" adds top class to the menu panel', () => {
    const out = html(
      <Menu
        open
        placement="top"
        label="账户"
        trigger="张"
        triggerLabel="账户菜单"
        entries={[{ key: 'a', label: '钱包', onSelect: noop }]}
      />,
    )
    expect(out).toMatch(/class="[^"]*top[^"]*"/)
  })
})
