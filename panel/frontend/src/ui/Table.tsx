import type { Key, KeyboardEvent, ReactNode } from 'react'
import { Checkbox } from './Checkbox'
import { cx } from './cx'

// 没有类名时不输出空的 class 属性
const cn = (...names: Parameters<typeof cx>): string | undefined => cx(...names) || undefined
import { Empty } from './Empty'
import { Skeleton } from './Skeleton'
import css from './Table.module.css'

export type SortDir = 'asc' | 'desc'

export interface TableSort {
  key: string
  dir: SortDir
}

export interface TableColumn<T> {
  key: string
  header: ReactNode
  render: (row: T) => ReactNode
  /** CSS 宽度，如 '90px' 或 '20%'；不给则自适应 */
  width?: string
  align?: 'left' | 'right'
  /** 节点名、订单号、IP 这类要逐字核对的列 */
  mono?: boolean
  /** 可点表头排序 */
  sortable?: boolean
  /** ≤639 时整列隐藏（样稿 .h-m），把次要信息留给桌面 */
  hideOnMobile?: boolean
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
  /** 行点击；传入则行可 Tab 聚焦，Enter / 空格触发同一回调 */
  onRowClick?: (row: T) => void
  /** 勾选：传入则第一列出现复选框 */
  selection?: {
    selected: ReadonlySet<Key>
    onChange: (next: Set<Key>) => void
  }
  /** 当前排序；配合列的 sortable */
  sort?: TableSort
  /** 点可排序的表头：同一列再点切换方向，换列时由调用方决定初始方向 */
  onSortChange?: (key: string) => void
  /** 表尾一行（样稿 .tfoot，48 高）：计数、分页按钮；内容自己用 flex 排，组件只给容器 */
  footer?: ReactNode
  /** 表头吸顶（表格放在自己滚动的容器里时用） */
  stickyHeader?: boolean
  /** 装进盒子（样稿 .box）：白底、1px 描边、圆角 12、溢出裁切；列表页一般都开 */
  boxed?: boolean
  /** 给某一行加 className（如「当前选中」「已归档置灰」）；返回 undefined 不加 */
  rowClassName?: (row: T) => string | undefined
}

export function Table<T>({
  columns,
  rows,
  rowKey,
  label,
  loading = false,
  empty,
  onRowClick,
  selection,
  sort,
  onSortChange,
  footer,
  stickyHeader,
  boxed,
  rowClassName,
}: TableProps<T>) {
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
    <div className={cn(css.root, boxed && css.boxed, footer != null && css.hasFooter)}>
      <div className={css.scroll}>
        <table className={css.table} aria-label={label} aria-busy={loading || undefined}>
          <colgroup>
            {selection && <col className={css.pickCol} />}
            {columns.map((c) => (
              <col key={c.key} className={cn(c.hideOnMobile && css.hideMobile)} style={c.width ? { width: c.width } : undefined} />
            ))}
          </colgroup>
          <thead>
            <tr>
              {selection && (
                <th scope="col" className={cn(stickyHeader && css.sticky)}>
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
              {columns.map((c) => {
                const canSort = Boolean(c.sortable && onSortChange)
                const isSorted = canSort && sort?.key === c.key
                const ariaSort = canSort ? (isSorted ? (sort?.dir === 'asc' ? 'ascending' : 'descending') : 'none') : undefined

                return (
                  <th
                    key={c.key}
                    scope="col"
                    aria-sort={ariaSort}
                    className={cn(
                      c.align === 'right' && css.right,
                      c.hideOnMobile && css.hideMobile,
                      stickyHeader && css.sticky,
                      isSorted && css.sorted,
                    )}
                  >
                    {canSort ? (
                      <button
                        type="button"
                        className={cn(css.sortButton, c.align === 'right' && css.sortButtonRight)}
                        onClick={() => onSortChange!(c.key)}
                      >
                        <span>{c.header}</span>
                        <svg
                          viewBox="0 0 24 24"
                          width={12}
                          height={12}
                          stroke="currentColor"
                          strokeWidth={2}
                          fill="none"
                          aria-hidden="true"
                          className={cn(
                            css.sortIcon,
                            isSorted && css.sortIconActive,
                            isSorted && sort?.dir === 'desc' && css.sortIconDesc,
                          )}
                        >
                          <path d="m18 15-6-6-6 6" />
                        </svg>
                      </button>
                    ) : (
                      c.header
                    )}
                  </th>
                )
              })}
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
                    className={cn(isPicked && css.picked, onRowClick && css.clickable, rowClassName?.(row))}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                    tabIndex={onRowClick ? 0 : undefined}
                    onKeyDown={onRowClick ? (e) => activateOnKey(e, () => onRowClick(row)) : undefined}
                  >
                    {selection && (
                      <td onClick={(e) => e.stopPropagation()}>
                        <Checkbox small aria-label="选择此行" checked={isPicked} onChange={() => toggle(key)} />
                      </td>
                    )}
                    {columns.map((c) => (
                      <td key={c.key} className={cn(c.align === 'right' && css.right, c.mono && 'mono', c.hideOnMobile && css.hideMobile)}>
                        {c.render(row)}
                      </td>
                    ))}
                  </tr>
                )
              })}
          </tbody>
        </table>
      </div>
      {footer != null && <div className={css.footer}>{footer}</div>}
    </div>
  )
}

/** 行上的 Enter / 空格等同点击；只认行本身得到的按键，行里的链接、按钮、复选框各管各的 */
function activateOnKey(e: KeyboardEvent<HTMLTableRowElement>, activate: () => void) {
  if (e.target !== e.currentTarget || (e.key !== 'Enter' && e.key !== ' ')) return
  e.preventDefault()
  activate()
}
