/**
 * [INPUT]: 依赖 react 的 useSyncExternalStore
 * [OUTPUT]: 对外提供 useNow
 * [POS]: portal/screens/account 的秒级时钟：快捷登录链接的 60 秒倒计时与 Telegram 绑定码的 10 分钟倒计时共用；只在有东西要倒数时订阅每秒一跳，平时不起定时器；读数取整到秒，同一秒内多次读取得到同一个值
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useSyncExternalStore } from 'react'

const everySecond = (onTick: () => void) => {
  const timer = setInterval(onTick, 1000)
  return () => clearInterval(timer)
}
const never = () => () => {}
const currentSecond = () => Math.floor(Date.now() / 1000) * 1000

export function useNow(active: boolean): number {
  return useSyncExternalStore(active ? everySecond : never, currentSecond)
}
