import { useEffect, useState } from 'react'

/** 值停止变化 ms 毫秒后才跟上（输入框驱动请求时用，免得每敲一个字发一次） */
export function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timer)
  }, [value, ms])
  return settled
}
