/**
 * [INPUT]: 依赖 react-dom/server 的 renderToStaticMarkup，依赖 ../core/api 的 ApiError，依赖 ./index 的全部组件
 * [OUTPUT]: 对外提供组件库的无障碍与结构测试
 * [POS]: ui 的单元测试：不引入 DOM 库，用服务端渲染核对角色、aria 属性与关键结构，Table 行的 Enter / 空格激活直接调用组件取元素树喂假按键事件；交互（方向键、弹层开合、焦点）在 showcase 里用浏览器验收
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { ReactElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { ApiError } from '../core/api'
import {
  Button,
  Checkbox,
  CountBadge,
  Drawer,
  Empty,
  Input,
  Menu,
  Modal,
  Pager,
  QueryView,
  Segmented,
  Select,
  Skeleton,
  StatStrip,
  Switch,
  Table,
  Tabs,
  TextArea,
} from './index'

const html = (node: ReactElement) => renderToStaticMarkup(node)
const noop = () => {}

describe('Button', () => {
  it('defaults to type="button" so it never submits a form by accident', () => {
    expect(html(<Button>导出</Button>)).toContain('type="button"')
  })

  it('busy disables the button and announces it', () => {
    const out = html(<Button busy>发布配置</Button>)
    expect(out).toContain('disabled=""')
    expect(out).toContain('aria-busy="true"')
  })
})

describe('form controls', () => {
  it('Input wires label, error message and aria attributes', () => {
    const out = html(<Input id="coupon" label="优惠码" error="优惠码已过期" />)
    expect(out).toContain('<label')
    expect(out).toContain('for="coupon"')
    expect(out).toContain('aria-invalid="true"')
    expect(out).toContain('aria-describedby="coupon-message"')
    expect(out).toMatch(/id="coupon-message"[^>]*role="alert"/)
  })

  it('Input without hint or error has no dangling aria-describedby', () => {
    expect(html(<Input label="邮箱" />)).not.toContain('aria-describedby')
  })

  it('TextArea shares the field wiring', () => {
    expect(html(<TextArea id="body" label="内容" hint="支持换行" />)).toContain('aria-describedby="body-message"')
  })

  it('Select renders a disabled placeholder and every option', () => {
    const out = html(
      <Select
        label="节点池"
        placeholder="请选择"
        defaultValue=""
        options={[
          { value: 'hk', label: '香港' },
          { value: 'jp', label: '东京', disabled: true },
        ]}
      />,
    )
    expect(out).toMatch(/<option value="" disabled=""[^>]*>请选择<\/option>/)
    expect(out).toContain('<option value="jp" disabled="">东京</option>')
  })

  it('Select emptyOption is a selectable empty value and replaces the placeholder', () => {
    const out = html(<Select label="用户组" emptyOption="全部用户组" placeholder="请选择" value="" onChange={noop} options={[{ value: 'vip', label: 'VIP' }]} />)
    expect(out).toMatch(/<option value="" selected="">全部用户组<\/option>/)
    expect(out).not.toContain('请选择')
    expect(out).not.toMatch(/<option value=""[^>]*disabled/)
  })

  it('an empty-string error counts as no error: no red state, no alert, hint stays', () => {
    const cleared = html(<Input id="n" label="名称" error="" hint="必填" />)
    expect(cleared).not.toContain('aria-invalid')
    expect(cleared).not.toContain('role="alert"')
    expect(cleared).toMatch(/id="n-message"[^>]*>必填</)
    expect(html(<Select id="s" label="组" error="" options={[]} />)).not.toContain('aria-invalid')
    expect(html(<TextArea id="t" label="原因" error="" />)).not.toContain('aria-describedby')
  })

  it('Switch is a real checkbox with the switch role', () => {
    const out = html(<Switch aria-label="启用" defaultChecked />)
    expect(out).toContain('type="checkbox"')
    expect(out).toContain('role="switch"')
  })

  it('Checkbox ties its label to the input with an explicit for/id, keeping a caller id', () => {
    const out = html(<Checkbox label="记住我" />)
    expect(out).toContain('type="checkbox"')
    const id = /<input[^>]*id="([^"]+)"/.exec(out)?.[1]
    expect(id).toBeTruthy()
    expect(out).toContain(`<label for="${id}"`)
    // 文字是 label 的直接子节点，不再包 span
    expect(out).toMatch(/<\/span>记住我<\/label>$/)
    expect(html(<Checkbox label="全选" id="pick-all" />)).toMatch(/<label for="pick-all"[\s\S]*id="pick-all"/)
  })
})

describe('CountBadge', () => {
  it('hides at zero and caps large numbers', () => {
    expect(html(<CountBadge count={0} />)).toBe('')
    expect(html(<CountBadge count={120} />)).toContain('99+')
  })
})

describe('Tabs and Segmented', () => {
  const items = [
    { value: 'list', label: '用户列表' },
    { value: 'groups', label: '用户组' },
  ] as const

  it('Tabs uses roving tabindex and aria-selected', () => {
    const out = html(<Tabs label="用户" items={items} value="groups" onChange={noop} idPrefix="users" />)
    expect(out).toContain('role="tablist"')
    expect(out).toMatch(/aria-selected="false"[^>]*tabindex="-1"/)
    expect(out).toMatch(/aria-selected="true"[^>]*tabindex="0"/)
    expect(out).toContain('aria-controls="users-groups"')
  })

  it('Segmented is a radio group', () => {
    const out = html(<Segmented label="周期" options={items} value="list" onChange={noop} />)
    expect(out).toContain('role="radiogroup"')
    expect(out).toMatch(/role="radio" aria-checked="true" tabindex="0"/)
  })
})

describe('Table', () => {
  const columns = [
    { key: 'name', header: '节点', render: (r: { id: string; name: string }) => r.name, mono: true },
    { key: 'op', header: '操作', render: () => '详情', align: 'right' as const },
  ]
  const rows = [
    { id: 'a', name: 'hk-01.edge' },
    { id: 'b', name: 'jp-03.edge' },
  ]

  it('renders a labelled semantic table', () => {
    const out = html(<Table label="节点列表" columns={columns} rows={rows} rowKey={(r) => r.id} />)
    expect(out).toContain('<table')
    expect(out).toContain('aria-label="节点列表"')
    expect(out).toContain('<th scope="col"')
    expect(out).toContain('hk-01.edge')
  })

  it('checks the header box when every row is selected', () => {
    const out = html(
      <Table
        label="节点列表"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        selection={{ selected: new Set(['a', 'b']), onChange: noop }}
      />,
    )
    expect(out).toMatch(/aria-label="全选"[^>]*checked=""/)
  })

  it('shows the empty state and the loading state', () => {
    expect(html(<Table label="x" columns={columns} rows={[]} rowKey={(r) => r.id} />)).toContain('暂无数据')
    expect(html(<Table label="x" columns={columns} rows={rows} rowKey={(r) => r.id} loading />)).toContain('aria-busy="true"')
  })

  it('makes rows focusable only when they have a click handler', () => {
    const plain = html(<Table label="x" columns={columns} rows={rows} rowKey={(r) => r.id} />)
    expect(plain).not.toContain('tabindex')
    const clickable = html(<Table label="x" columns={columns} rows={rows} rowKey={(r) => r.id} onRowClick={noop} />)
    expect(clickable.match(/<tr[^>]*tabindex="0"/g)).toHaveLength(rows.length)
    // 加载与空状态的占位行不是数据行，不可聚焦
    expect(html(<Table label="x" columns={columns} rows={rows} rowKey={(r) => r.id} onRowClick={noop} loading />)).not.toContain('tabindex')
    expect(html(<Table label="x" columns={columns} rows={[]} rowKey={(r) => r.id} onRowClick={noop} />)).not.toContain('tabindex')
  })

  it('activates a focused row with Enter or Space, and leaves keys inside the row alone', () => {
    const hits: string[] = []
    // Table 没有 hook，直接调用拿元素树，取出数据行的 onKeyDown
    const tree = Table({ label: 'x', columns, rows, rowKey: (r) => r.id, onRowClick: (r) => hits.push(r.id) })
    const rowsOf = (node: unknown): ReactElement<RowProps>[] => {
      if (Array.isArray(node)) return node.flatMap(rowsOf)
      if (!node || typeof node !== 'object' || !('props' in node)) return []
      const el = node as ReactElement<RowProps & { children?: unknown }>
      return el.type === 'tr' && el.props.tabIndex === 0 ? [el] : rowsOf(el.props.children)
    }
    const [first, second] = rowsOf(tree)
    const press = (row: ReactElement<RowProps> | undefined, key: string, fromChild = false) => {
      let prevented = false
      const self = {}
      row!.props.onKeyDown!({ key, currentTarget: self, target: fromChild ? {} : self, preventDefault: () => (prevented = true) })
      return prevented
    }
    expect(press(first, 'Enter')).toBe(true)
    expect(press(second, ' ')).toBe(true)
    expect(press(first, 'a')).toBe(false)
    expect(press(first, 'Enter', true)).toBe(false)
    expect(hits).toEqual(['a', 'b'])
  })
})

interface RowProps {
  tabIndex?: number
  onKeyDown?: (e: { key: string; currentTarget: object; target: object; preventDefault: () => void }) => void
}

describe('overlays', () => {
  it('Modal keeps its content out of the DOM while closed', () => {
    const out = html(
      <Modal open={false} onClose={noop} title="退出登录？">
        当前设备需要重新登录
      </Modal>,
    )
    expect(out).toContain('<dialog')
    expect(out).not.toContain('退出登录？')
  })

  it('Modal labels the dialog with its title', () => {
    const out = html(
      <Modal open onClose={noop} title="退出登录？">
        当前设备需要重新登录
      </Modal>,
    )
    const labelledBy = /aria-labelledby="([^"]+)"/.exec(out)?.[1]
    expect(labelledBy).toBeTruthy()
    expect(out).toContain(`id="${labelledBy}"`)
  })

  it('Drawer has a labelled close button', () => {
    expect(html(<Drawer open onClose={noop} title="PD-2609-3312" />)).toContain('aria-label="关闭"')
  })

  it('Menu starts closed with a menu button', () => {
    const out = html(<Menu label="账户" trigger="张" triggerLabel="账户菜单" entries={[{ key: 'a', label: '钱包', onSelect: noop }]} />)
    expect(out).toContain('aria-haspopup="menu"')
    expect(out).toContain('aria-expanded="false"')
    expect(out).not.toContain('role="menu"')
  })
})

describe('feedback', () => {
  it('Skeleton is hidden from assistive technology', () => {
    expect(html(<Skeleton />)).toContain('aria-hidden="true"')
  })

  it('Empty renders title, description and one action', () => {
    const out = html(<Empty title="还没有工单" description="遇到问题可以在这里联系客服。" action={<Button>新建工单</Button>} />)
    expect(out).toContain('还没有工单')
    expect(out).toContain('新建工单')
  })
})

describe('data views', () => {
  const query = <T,>(over: Partial<{ data: T; isPending: boolean; isError: boolean; error: unknown }>) => ({
    data: undefined as T | undefined,
    isPending: false,
    isError: false,
    error: null,
    refetch: noop,
    ...over,
  })

  it('StatStrip is a named group and draws skeleton cells until the numbers arrive', () => {
    const loading = html(<StatStrip label="礼品卡统计" items={undefined} count={3} />)
    expect(loading).toContain('role="group"')
    expect(loading).toContain('aria-label="礼品卡统计"')
    expect(loading).toContain('aria-busy="true"')
    expect(loading.match(/aria-hidden="true"/g)).toHaveLength(6)
    const ready = html(<StatStrip label="佣金统计" items={[{ label: '冻结中', value: '¥3,904.00' }]} />)
    expect(ready).toContain('冻结中')
    expect(ready).toContain('¥3,904.00')
    expect(ready).not.toContain('aria-busy')
  })

  it('Pager hides within one page and disables the ends', () => {
    expect(html(<Pager total={50} limit={50} offset={0} onChange={noop} />)).toBe('')
    const first = html(<Pager total={120} limit={50} offset={0} onChange={noop} />)
    expect(first).toContain('aria-label="分页"')
    expect(first).toContain('第 1 / 3 页 · 共 120 条')
    expect(first.match(/disabled=""/g)).toHaveLength(1)
    const last = html(<Pager total={120} limit={50} offset={100} onChange={noop} />)
    expect(last).toMatch(/<button[^>]*disabled=""[^>]*>下一页/)
  })

  it('QueryView renders loading, 404 as missing-or-forbidden, other errors with retry, empty, then data', () => {
    const view = (q: ReturnType<typeof query<string[]>>) => html(<QueryView query={q} empty={<p>空的</p>}>{(rows) => <p>{rows.join(',')}</p>}</QueryView>)
    expect(view(query({ isPending: true }))).toContain('aria-label="加载中"')
    const missing = view(query({ isError: true, error: new ApiError({ status: 404, code: 'not_found', message: '资源不存在或无权访问' }) }))
    expect(missing).toContain('无权限或不存在')
    expect(missing).not.toContain('重试')
    const broken = view(query({ isError: true, error: new ApiError({ status: 500, code: 'internal_error', message: '服务暂时不可用' }) }))
    expect(broken).toContain('服务暂时不可用')
    expect(broken).toContain('重试')
    expect(view(query({ data: [] }))).toBe('<p>空的</p>')
    expect(view(query({ data: ['a', 'b'] }))).toBe('<p>a,b</p>')
    const custom = html(<QueryView query={query({ data: { total: 0 } })} isEmpty={(d) => d.total === 0} empty={<p>无</p>}>{() => <p>有</p>}</QueryView>)
    expect(custom).toBe('<p>无</p>')
  })
})
