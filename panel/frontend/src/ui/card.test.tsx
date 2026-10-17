import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { Card } from './index'

const html = (node: ReactElement) => renderToStaticMarkup(node)

describe('Card', () => {
  it('renders children without header when title and extra are omitted', () => {
    const out = html(
      <Card>
        <div>主体内容</div>
      </Card>,
    )
    expect(out).not.toContain('<header')
    expect(out).toContain('主体内容')
  })

  it('renders header with title, subtitle, and extra when provided', () => {
    const out = html(
      <Card title="流量排行" subtitle="近 24 小时" extra={<button type="button">x</button>}>
        <div>内容</div>
      </Card>,
    )
    expect(out).toContain('<header')
    expect(out).toContain('<h3')
    expect(out).toContain('流量排行')
    expect(out).toContain('近 24 小时')
    expect(out).toContain('<button type="button">x</button>')
  })

  it('renders footer when provided', () => {
    const out = html(
      <Card footer={<span>只影响之后产生的佣金</span>}>
        <div>内容</div>
      </Card>,
    )
    expect(out).toContain('<footer')
    expect(out).toContain('只影响之后产生的佣金')
  })
})
