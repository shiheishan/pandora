/**
 * [INPUT]: 依赖 react 的 useEffect、useRef 与事件类型
 * [OUTPUT]: 对外提供 useModalDialog：把受控的 open 同步到原生 <dialog> 的 showModal() / close()，把 Esc 与点遮罩统一成 onClose，并决定打开时的初始焦点
 * [POS]: ui 的弹层内核，Modal 与 Drawer 共用；用原生 <dialog> 是为了让浏览器负责顶层渲染、焦点圈定与背景 inert，不自己写焦点陷阱
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useRef, type MouseEvent, type SyntheticEvent } from 'react'

export function useModalDialog(open: boolean, onClose: () => void, dismissible: boolean) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    if (open && !dialog.open) {
      dialog.showModal()
      // 初始焦点：浏览器默认落在第一个可聚焦元素上（常是「取消」），而且会画出键盘焦点圈。
      // 改成落在调用方标了 data-autofocus 的元素（如重新验证身份的密码框），否则落在面板本身。
      const target = dialog.querySelector<HTMLElement>('[data-autofocus]') ?? dialog.querySelector<HTMLElement>('[data-dialog-panel]')
      target?.focus()
    }
    if (!open && dialog.open) dialog.close()
  }, [open])

  // 卸载时如果还开着，关掉它，否则页面会一直处于 inert
  useEffect(() => {
    const dialog = ref.current
    return () => {
      if (dialog?.open) dialog.close()
    }
  }, [])

  return {
    ref,
    // Esc：浏览器默认会直接关闭 <dialog>，这里拦下来交给调用方决定（受控）
    onCancel: (event: SyntheticEvent<HTMLDialogElement>) => {
      event.preventDefault()
      if (dismissible) onClose()
    },
    // <dialog> 铺满视口，面板之外的区域就是遮罩：点在 dialog 自身上即点在遮罩上
    onClick: (event: MouseEvent<HTMLDialogElement>) => {
      if (dismissible && event.target === event.currentTarget) onClose()
    },
  }
}
