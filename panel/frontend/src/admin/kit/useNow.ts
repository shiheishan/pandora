import { useEffect, useState } from 'react'

/** 当前时刻，按间隔（缺省每分钟）走一次：相对时间、等待时长、到期天数用它，不在渲染里读时钟 */
export function useNow(intervalMs = 60_000): Date {
  const [now, setNow] = useState(() => new Date())
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs)
    return () => clearInterval(timer)
  }, [intervalMs])
  return now
}
