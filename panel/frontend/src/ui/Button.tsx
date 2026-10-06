import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cx } from './cx'
import css from './Button.module.css'

// ---------------------------------------------------------------------------
// variant 按规范「按钮」一行：
//   primary   实心强调色（门户朱砂、后台墨色），每个视图最多一个
//   outline   强调色描边，门户需要第二个强调时用
//   secondary 白底描边，最常见的次按钮
//   ghost     无底无框的弱操作（「稍后支付」）
//   danger    实心危险色（「吊销身份」「退出」）
//   link      文字链接样式的按钮（后台「查看详情」）
// size 对应角色令牌的三档高度：md = --h-control，sm = --h-control-sm，xs = --h-control-xs；
// dialog 是弹窗底部按钮（门户 36、后台 32），由 Modal / Drawer 的操作区使用。
// ---------------------------------------------------------------------------
export type ButtonVariant = 'primary' | 'outline' | 'secondary' | 'ghost' | 'danger' | 'link'
export type ButtonSize = 'md' | 'sm' | 'xs' | 'dialog'

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
  /** 进行中：禁用并显示转圈，屏幕阅读器读到 aria-busy */
  busy?: boolean
  /** 窄屏弹窗里的按钮通栏 */
  block?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', busy = false, block = false, disabled, type = 'button', className, children, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={cx(css.button, css[variant], css[size], block && css.block, busy && css.busy, className)}
      {...rest}
    >
      {busy && <span className={css.spinner} aria-hidden="true" />}
      {children}
    </button>
  )
})
