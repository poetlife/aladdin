import { useEffect, useRef } from 'react'

/**
 * 焦点与可见性接连触发时，合并成一次重载的时间窗。
 *
 * 切回这个标签时，浏览器通常会先发 `visibilitychange` 再发 `focus`（或反过来）。
 * 两次都去拉草稿，状态条会闪两次「校验中」。
 */
const COALESCE_MS = 50

/**
 * 页面重新可见或窗口重新获得焦点时调用 `reload`。
 *
 * 网页端不改内容，命令行却可以在这一页开着时把新草稿 push 上来。回到这一页时
 * 重拉，状态条才不会停在上一次的「可以发布」上（见 docs/design/galaxy/authoring.md）。
 * 页面仍处于隐藏时不调用——切走不是"回到这一页"。
 *
 * `enabled` 为假时不监听（例如首屏还没加载完）。`reload` 始终用最新的那一份，
 * 避免每次渲染都拆掉监听。
 */
export function useReloadWhenVisible(enabled: boolean, reload: () => void): void {
  const reloadRef = useRef(reload)
  reloadRef.current = reload

  useEffect(() => {
    if (!enabled) {
      return
    }
    let lastStartedAt = 0
    const schedule = (): void => {
      if (document.visibilityState === 'hidden') {
        return
      }
      const now = Date.now()
      if (now - lastStartedAt < COALESCE_MS) {
        return
      }
      lastStartedAt = now
      reloadRef.current()
    }
    const onVisibility = (): void => {
      if (document.visibilityState === 'visible') {
        schedule()
      }
    }
    document.addEventListener('visibilitychange', onVisibility)
    window.addEventListener('focus', schedule)
    return () => {
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('focus', schedule)
    }
  }, [enabled])
}
