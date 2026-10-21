import { useLayoutEffect, useRef, useState, type ButtonHTMLAttributes, type MouseEvent, type RefObject } from 'react'

/** 显示 / 隐藏两种状态对应的输入框类型与按钮文案（纯函数，单测直接测它） */
export function revealState(shown: boolean): { type: 'text' | 'password'; label: '隐藏密码' | '显示密码'; pressed: boolean } {
  return shown ? { type: 'text', label: '隐藏密码', pressed: true } : { type: 'password', label: '显示密码', pressed: false }
}

/**
 * 密码框的「显示 / 隐藏」开关：缺省隐藏，状态只在组件内存里（不写 localStorage）。
 * 用鼠标点按钮时 mousedown 阻止默认行为，焦点留在输入框；切换前记下光标与选区，换完类型后还原（有的浏览器换 type 会把光标挪到开头）。
 * 用键盘（Tab 到按钮再按 Enter / 空格）切换时焦点留在按钮上，原生 button 自带这两个键。
 */
export function usePasswordReveal(inputRef: RefObject<HTMLInputElement | null>) {
  const [shown, setShown] = useState(false)
  const caret = useRef<{ start: number | null; end: number | null } | null>(null)
  useLayoutEffect(() => {
    const saved = caret.current
    const el = inputRef.current
    caret.current = null
    if (!saved || !el || document.activeElement !== el) return
    el.setSelectionRange(saved.start, saved.end)
  }, [shown, inputRef])
  const toggle = () => {
    const el = inputRef.current
    if (el && document.activeElement === el) caret.current = { start: el.selectionStart, end: el.selectionEnd }
    setShown((v) => !v)
  }
  const state = revealState(shown)
  const buttonProps: ButtonHTMLAttributes<HTMLButtonElement> = {
    type: 'button',
    'aria-label': state.label,
    'aria-pressed': state.pressed,
    title: state.label,
    onMouseDown: (e: MouseEvent<HTMLButtonElement>) => e.preventDefault(),
    onClick: toggle,
  }
  return { shown, type: state.type, buttonProps }
}
