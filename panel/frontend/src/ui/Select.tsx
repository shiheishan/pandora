import {
  forwardRef,
  useCallback,
  useEffect,
  useId,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
} from 'react'
import control from './control.module.css'
import { cx } from './cx'
import { Field, hasError, useFieldIds, type FieldControlProps } from './Field'
import { IconCheck, IconChevronDown } from './icons'
import css from './Select.module.css'
import { moveActive, placeList, typeahead } from './select-logic'

export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

/** 与原生 select 的 onChange 同形：调用方照旧写 e.target.value */
export interface SelectChangeEvent {
  target: { value: string; name: string }
  currentTarget: { value: string; name: string }
}

export interface SelectProps extends FieldControlProps {
  options: readonly SelectOption[]
  value?: string
  defaultValue?: string
  onChange?: (event: SelectChangeEvent) => void
  /** 未选择时显示的占位文字，值为空串、不在列表里（必填的下拉用） */
  placeholder?: string
  /** 可选的空值项（如「不限」「全部用户组」），值为空串、排在列表最前；传了就不再用 placeholder */
  emptyOption?: string
  size?: 'md' | 'sm'
  disabled?: boolean
  id?: string
  name?: string
  title?: string
  'aria-label'?: string
  className?: string
  fieldClassName?: string
}

export const Select = forwardRef<HTMLButtonElement, SelectProps>(function Select(
  {
    label,
    hint,
    error,
    options,
    value: controlledValue,
    defaultValue,
    onChange,
    placeholder,
    emptyOption,
    size = 'md',
    disabled = false,
    id,
    name,
    title,
    'aria-label': ariaLabel,
    className,
    fieldClassName,
  },
  forwardedRef,
) {
  const { controlId, messageId } = useFieldIds(id)
  const listId = useId()

  const buttonRef = useRef<HTMLButtonElement>(null)
  useImperativeHandle(forwardedRef, () => buttonRef.current!)

  const listRef = useRef<HTMLDivElement>(null)

  // 受控 / 不受控状态
  const isControlled = controlledValue !== undefined
  const [internalValue, setInternalValue] = useState<string>(() => defaultValue ?? '')
  const currentValue = isControlled ? controlledValue : internalValue

  // 处理选项列表：如果提供了 emptyOption，排在最前
  const allOptions = useMemo<readonly SelectOption[]>(() => {
    if (emptyOption != null) {
      return [{ value: '', label: emptyOption }, ...options]
    }
    return options
  }, [emptyOption, options])

  // 当前选中项及 label
  const selectedOption = useMemo(
    () => allOptions.find((o) => o.value === currentValue),
    [allOptions, currentValue],
  )

  const [open, setOpen] = useState(false)
  const [activeIdx, setActiveIdx] = useState<number>(-1)
  const [coords, setCoords] = useState<{ top: number; left: number; width: number; maxHeight: number; up: boolean }>({
    top: 0,
    left: 0,
    width: 140,
    maxHeight: 280,
    up: false,
  })

  // 首字母查找缓冲
  const typeaheadBufferRef = useRef('')
  const typeaheadTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const triggerPlaceholder = emptyOption == null && placeholder != null && !selectedOption

  const close = useCallback(() => {
    setOpen(false)
  }, [])

  const openMenu = useCallback(
    (initialActiveIdx?: number) => {
      if (disabled) return
      const btn = buttonRef.current
      if (btn) {
        const rect = btn.getBoundingClientRect()
        const pos = placeList(rect, window.innerHeight, allOptions.length)
        setCoords(pos)
      }

      // 确定初始活动项
      let actIdx = -1
      if (initialActiveIdx !== undefined) {
        actIdx = initialActiveIdx
      } else {
        const curIdx = allOptions.findIndex((o) => o.value === currentValue)
        if (curIdx >= 0 && !allOptions[curIdx]!.disabled) {
          actIdx = curIdx
        } else {
          actIdx = allOptions.findIndex((o) => !o.disabled)
        }
      }

      setActiveIdx(actIdx)
      setOpen(true)
    },
    [allOptions, currentValue, disabled],
  )

  const selectOption = useCallback(
    (opt: SelectOption) => {
      if (opt.disabled) return
      if (opt.value !== currentValue) {
        if (!isControlled) {
          setInternalValue(opt.value)
        }
        onChange?.({
          target: { value: opt.value, name: name ?? '' },
          currentTarget: { value: opt.value, name: name ?? '' },
        })
      }
      close()
      buttonRef.current?.focus()
    },
    [currentValue, isControlled, name, onChange, close],
  )

  // 卸载时清掉首字母输入的计时器
  useEffect(
    () => () => {
      if (typeaheadTimerRef.current) clearTimeout(typeaheadTimerRef.current)
    },
    [],
  )

  // 活动项发生变化时滚动到可视区域
  useEffect(() => {
    if (!open || activeIdx < 0) return
    const listEl = listRef.current
    if (!listEl) return
    const optionEl = listEl.children[activeIdx] as HTMLElement | undefined
    optionEl?.scrollIntoView({ block: 'nearest' })
  }, [open, activeIdx])

  // 打开时的滚动/窗口变化/外部点击监听
  useEffect(() => {
    if (!open) return

    const onScroll = (e: Event) => {
      const listEl = listRef.current
      if (listEl && listEl.contains(e.target as Node)) return
      close()
    }

    const onResize = () => {
      close()
    }

    const onPointerDown = (e: PointerEvent) => {
      const target = e.target as Node
      if (buttonRef.current?.contains(target)) return
      if (listRef.current?.contains(target)) return
      close()
    }

    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onResize)
    window.addEventListener('pointerdown', onPointerDown)

    return () => {
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('resize', onResize)
      window.removeEventListener('pointerdown', onPointerDown)
    }
  }, [open, close])

  const handleTriggerClick = (e: MouseEvent) => {
    e.preventDefault()
    if (disabled) return
    if (open) {
      close()
    } else {
      openMenu()
    }
  }

  const handleTriggerKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return

    if (!open) {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
        e.preventDefault()
        openMenu()
        return
      }
      if (e.key === 'Home') {
        e.preventDefault()
        openMenu(moveActive(allOptions, -1, 'Home'))
        return
      }
      if (e.key === 'End') {
        e.preventDefault()
        openMenu(moveActive(allOptions, -1, 'End'))
        return
      }
      // 可打印字符打开并做首字母跳转
      if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault()
        const next = typeahead(allOptions, -1, e.key)
        openMenu(next)
        typeaheadBufferRef.current = e.key.toLowerCase()
        if (typeaheadTimerRef.current) clearTimeout(typeaheadTimerRef.current)
        typeaheadTimerRef.current = setTimeout(() => {
          typeaheadBufferRef.current = ''
        }, 500)
        return
      }
      return
    }

    // 已打开状态
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      close()
      return
    }

    if (e.key === 'Tab') {
      // Tab 收起、不改值、不拦默认行为（焦点照常移走）
      close()
      return
    }

    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Home' || e.key === 'End' || e.key === 'PageDown' || e.key === 'PageUp') {
      e.preventDefault()
      const nextIdx = moveActive(allOptions, activeIdx, e.key)
      if (nextIdx >= 0) {
        setActiveIdx(nextIdx)
      }
      return
    }

    if (e.key === 'Enter' || e.key === ' ') {
      // Enter 或空格（不在首字母输入中途时）选中活动项并收起
      // 首字母输入中途的空格算作输入的一部分，不当成选择
      if (!(e.key === ' ' && typeaheadBufferRef.current)) {
        e.preventDefault()
        const picked = allOptions[activeIdx]
        if (picked) selectOption(picked)
        return
      }
    }

    // 首字母搜索 (可打印单字符)
    if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      e.preventDefault()
      const char = e.key.toLowerCase()
      typeaheadBufferRef.current += char
      if (typeaheadTimerRef.current) clearTimeout(typeaheadTimerRef.current)
      typeaheadTimerRef.current = setTimeout(() => {
        typeaheadBufferRef.current = ''
      }, 500)

      const next = typeahead(allOptions, activeIdx, typeaheadBufferRef.current)
      if (next >= 0) {
        setActiveIdx(next)
      }
    }
  }

  const activeOptionId = open && activeIdx >= 0 ? `${listId}-opt-${activeIdx}` : undefined

  return (
    <Field
      label={label}
      hint={hint}
      error={error}
      controlId={controlId}
      messageId={messageId}
      className={fieldClassName}
    >
      <button
        ref={buttonRef}
        type="button"
        role="combobox"
        id={controlId}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={activeOptionId}
        aria-invalid={hasError(error) || undefined}
        aria-describedby={hasError(error) || hint != null ? messageId : undefined}
        aria-label={ariaLabel}
        title={title}
        disabled={disabled}
        onClick={handleTriggerClick}
        onKeyDown={handleTriggerKeyDown}
        className={cx(control.control, size === 'sm' && control.sm, css.trigger, className)}
      >
        <span className={cx(css.text, triggerPlaceholder && css.placeholder)}>
          {selectedOption ? selectedOption.label : placeholder ?? ''}
        </span>
        <IconChevronDown
          className={cx(css.chevron, open && css.chevronOpen)}
          size={14}
        />
      </button>

      {name != null && <input type="hidden" name={name} value={currentValue} />}

      {open && (
        <div
          role="listbox"
          id={listId}
          aria-labelledby={controlId}
          ref={listRef}
          className={cx(css.list, coords.up && css.up)}
          style={{
            top: coords.top,
            left: coords.left,
            width: coords.width,
            maxHeight: coords.maxHeight,
          }}
        >
          {allOptions.map((opt, idx) => {
            const isSelected = opt.value === currentValue
            const isActive = idx === activeIdx
            const optId = `${listId}-opt-${idx}`

            return (
              <div
                key={opt.value}
                id={optId}
                role="option"
                aria-selected={isSelected}
                aria-disabled={opt.disabled || undefined}
                onPointerEnter={() => {
                  if (!opt.disabled) {
                    setActiveIdx(idx)
                  }
                }}
                onClick={(e) => {
                  e.stopPropagation()
                  selectOption(opt)
                }}
                className={cx(
                  css.option,
                  isSelected && css.selected,
                  isActive && css.active,
                  opt.disabled && css.disabled,
                )}
              >
                <span className={css.optionLabel}>{opt.label}</span>
                {isSelected && <IconCheck className={css.check} size={14} />}
              </div>
            )
          })}
        </div>
      )}
    </Field>
  )
})
