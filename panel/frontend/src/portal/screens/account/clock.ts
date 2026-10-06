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
