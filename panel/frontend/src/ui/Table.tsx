/**
 * [INPUT]: 依赖 react 的 ReactNode 与 Key，依赖 ./Checkbox、./Empty、./Skeleton、./cx、./Table.module.css
 * [OUTPUT]: 对外提供 Table 组件与 TableColumn、TableProps 类型
 * [POS]: ui 的数据表：语义化 <table>，列由 columns 描述；可选行勾选（表头三态）、行点击、加载骨架与空状态；窄屏横向滚动而不是挤压列
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import type { Key, ReactNode } from 'react'
import { Checkbox } from './Checkbox'
import { cx } from './cx'
import { Empty } from './Empty'
import { Skeleton } from './Skeleton'
import css from './Table.module.css'

export interface TableColumn<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  /** CSS 宽度，如 '90px' 或 '20%'；不给则自适应 */
  width?: string
  align?: 'left' | 'right'
  /** 节点名、订单号、IP 这类要逐字核对的列 */
  mono?: boolean
}

export interface TableProps<T> {
  columns: readonly TableColumn<T>[]
  rows: readonly T[]
  rowKey: (row: T) => Key
  /** 表格的无障碍名称，读屏用 */
  label: string
  loading?: boolean
  /** 空状态；不给则用默认一句 */
  empty?: ReactNode
  onRowClick?: (row: T) => void
  /** 勾选：传入则第一列出现复选框 */
  selection?: {
    selected: ReadonlySet<Key>
    onChange: (next: Set<Key>) => void
  }
}

export function Table<T>({ columns, rows, rowKey, label, loading = false, empty, onRowClick, selection }: TableProps<T>) {
  const keys = rows.map(rowKey)
  const picked = selection ? keys.filter((k) => selection.selected.has(k)).length : 0
  const toggleAll = () => selection?.onChange(new Set(picked === keys.length ? [] : keys))
  const toggle = (key: Key) => {
    if (!selection) return
    const next = new Set(selection.selected)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    selection.onChange(next)
  }
  const span = columns.length + (selection ? 1 : 0)

  return (
    <div className={css.scroll}>
      <table className={css.table} aria-label={label} aria-busy={loading || undefined}>
        <colgroup>
          {selection && <col className={css.pickCol} />}
          {columns.map((c) => (
            <col key={c.key} style={c.width ? { width: c.width } : undefined} />
          ))}
        </colgroup>
        <thead>
          <tr>
            {selection && (
              <th scope="col">
                <Checkbox
                  small
                  aria-label="全选"
                  checked={keys.length > 0 && picked === keys.length}
                  indeterminate={picked > 0 && picked < keys.length}
                  disabled={keys.length === 0}
                  onChange={toggleAll}
                />
              </th>
            )}
            {columns.map((c) => (
              <th key={c.key} scope="col" className={cx(c.align === 'right' && css.right)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {loading &&
            [0, 1, 2].map((i) => (
              <tr key={`loading-${i}`}>
                <td colSpan={span}>
                  <Skeleton height={14} width={`${70 - i * 15}%`} />
                </td>
              </tr>
            ))}
          {!loading && rows.length === 0 && (
            <tr>
              <td colSpan={span} className={css.emptyCell}>
                {empty ?? <Empty title="暂无数据" bare />}
              </td>
            </tr>
          )}
          {!loading &&
            rows.map((row, i) => {
              const key = keys[i]!
              const isPicked = selection?.selected.has(key) ?? false
              return (
                <tr
                  key={key}
                  className={cx(isPicked && css.picked, onRowClick && css.clickable)}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                >
                  {selection && (
                    <td onClick={(e) => e.stopPropagation()}>
                      <Checkbox small aria-label="选择此行" checked={isPicked} onChange={() => toggle(key)} />
                    </td>
                  )}
                  {columns.map((c) => (
                    <td key={c.key} className={cx(c.align === 'right' && css.right, c.mono && 'mono')}>
                      {c.render(row)}
                    </td>
                  ))}
                </tr>
              )
            })}
        </tbody>
      </table>
    </div>
  )
}
