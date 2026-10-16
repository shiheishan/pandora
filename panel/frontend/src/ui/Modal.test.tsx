import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Button } from './Button'
import { Modal } from './Modal'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('Modal', () => {
  it('size={640} renders panel with width:640px', () => {
    const out = html(<Modal open onClose={noop} title="测试" size={640} />)
    expect(out).toMatch(/style="[^"]*width:\s*640px[^"]*"/)
  })

  it('size="md" resolves to width:560px', () => {
    const out = html(<Modal open onClose={noop} title="测试" size="md" />)
    expect(out).toMatch(/style="[^"]*width:\s*560px[^"]*"/)
  })

  it('defaults to width:400px when size is omitted', () => {
    const out = html(<Modal open onClose={noop} title="测试" />)
    expect(out).toMatch(/style="[^"]*width:\s*400px[^"]*"/)
  })

  it('renders subtitle after title in head', () => {
    const out = html(<Modal open onClose={noop} title="主标题" subtitle="副标题说明" />)
    const titleIndex = out.indexOf('主标题')
    const subtitleIndex = out.indexOf('副标题说明')
    expect(titleIndex).toBeGreaterThan(-1)
    expect(subtitleIndex).toBeGreaterThan(titleIndex)
  })

  it('renders footer when footerStart or actions are provided', () => {
    const both = html(
      <Modal
        open
        onClose={noop}
        title="测试"
        footerStart={<Button>作废</Button>}
        actions={<Button>保存</Button>}
      />,
    )
    expect(both).toContain('作废')
    expect(both).toContain('保存')

    const onlyStart = html(
      <Modal open onClose={noop} title="测试" footerStart={<Button>作废</Button>} />,
    )
    expect(onlyStart).toContain('作废')
  })

  it('tall applies tall class to panel', () => {
    const out = html(<Modal open onClose={noop} title="详情" tall />)
    expect(out).toMatch(/class="[^"]*tall[^"]*"/)
  })
})
