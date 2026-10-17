import type { ReactNode } from 'react'
import { href } from '../../../core/router'
import { Btn, IconDisc } from '../../kit'
import css from './Result.module.css'

// ---------------------------------------------------------------------------
// 完成页（原型 scrResult）：「发生了什么 / 你要做的 / 没变的」三块；新开一份、换新链接、兑换开新份时
// 把「下一步：新链接 + 添加到 App」放在最上面。确认页付完、兑换卡、换新链接都用它。
// ---------------------------------------------------------------------------
export interface ResultInfo {
  title: string
  /** 「下一步」卡：有它就不写「你要做的：什么都不用做」 */
  next?: ReactNode
  happened: ReactNode[]
  kept: ReactNode[]
  /** 退路一句（「想换回来随时可以…」） */
  changeable?: string
}

export function ResultView({ info }: { info: ResultInfo }) {
  return (
    <div className={css.page} data-screen="result">
      <div className={css.head}>
        <IconDisc name="check" tone="solid" />
        <h2 className={css.title}>{info.title}</h2>
      </div>
      {info.next && (
        <div className={css.next}>
          <h3 className={css.label}>下一步</h3>
          {info.next}
        </div>
      )}
      <div className={css.card}>
        <h3 className={css.label}>发生了什么</h3>
        <ul className={css.list}>
          {info.happened.map((x, i) => (
            <li key={i}>{x}</li>
          ))}
        </ul>
        {!info.next && (
          <>
            <h3 className={css.label}>你要做的</h3>
            <p className={css.text}>什么都不用做。</p>
          </>
        )}
        {info.kept.length > 0 && (
          <>
            <h3 className={css.label}>没变的</h3>
            <ul className={css.list}>
              {info.kept.map((x, i) => (
                <li key={i}>{x}</li>
              ))}
            </ul>
          </>
        )}
        {info.changeable && <p className={css.again}>↺ {info.changeable}</p>}
      </div>
      <Btn href={href('/subs')} full id="btn-done">
        回到我的套餐
      </Btn>
    </div>
  )
}
