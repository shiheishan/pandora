import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it, vi } from 'vitest'
import { Table, type TableColumn, type TableSort } from './Table'

const html = (node: ReactElement) => renderToStaticMarkup(node)

describe('Table sorting and footer', () => {
  interface Item {
    id: string
    name: string
    count: number
    action: string
  }

  const columns: TableColumn<Item>[] = [
    { key: 'name', header: '名称', render: (r) => r.name, sortable: true },
    { key: 'count', header: '数量', render: (r) => r.count, sortable: true, align: 'right' },
    { key: 'action', header: '操作', render: (r) => r.action },
  ]

  const rows: Item[] = [
    { id: '1', name: 'alpha', count: 10, action: 'view' },
    { id: '2', name: 'beta', count: 20, action: 'edit' },
  ]

  it('renders sortable headers with button, aria-sort and directional styles', () => {
    const sort: TableSort = { key: 'name', dir: 'asc' }
    const onSortChange = vi.fn()

    const out = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        sort={sort}
        onSortChange={onSortChange}
      />,
    )

    // 当前排序列 name：aria-sort="ascending"，包含 button
    expect(out).toMatch(/<th[^>]*aria-sort="ascending"[^>]*><button[^>]*><span>名称<\/span>/)

    // 另一个可排序列 count：aria-sort="none"，包含 button
    expect(out).toMatch(/<th[^>]*aria-sort="none"[^>]*><button[^>]*><span>数量<\/span>/)

    // 不可排序列 action：没有 aria-sort 也没有 button
    expect(out).toMatch(/<th scope="col">操作<\/th>/)
    expect(out).not.toContain('>操作</button>')
  })

  it('sets aria-sort="descending" and rotates arrow when descending', () => {
    const sort: TableSort = { key: 'count', dir: 'desc' }
    const onSortChange = vi.fn()

    const out = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        sort={sort}
        onSortChange={onSortChange}
      />,
    )

    expect(out).toMatch(/<th[^>]*aria-sort="descending"/)
    // 降序时箭头有 sortIconDesc
    expect(out).toContain('sortIconDesc')
  })

  it('does not render sort button or aria-sort when onSortChange is omitted', () => {
    const out = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
      />,
    )

    expect(out).not.toContain('aria-sort')
    expect(out).not.toContain('<button')
    expect(out).toContain('<th scope="col">名称</th>')
  })

  it('renders footer when provided and omits footer container when not provided', () => {
    const withoutFooter = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
      />,
    )
    expect(withoutFooter).not.toContain('共 2 项')

    const withFooter = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        footer="共 2 项 · 已完成 1"
      />,
    )
    expect(withFooter).toContain('共 2 项 · 已完成 1')
  })

  it('supports stickyHeader prop', () => {
    const out = html(
      <Table
        label="测试列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        stickyHeader
      />,
    )
    expect(out).toContain('sticky')
  })
})
