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
 * 它是**兜底**，主路径是订阅（`web/src/watch/use-watch.ts`）：别处的改动会推一条事件过来，
 * 不必等谁做任何事。留着它的理由是浏览器会冻结后台标签页——那条流可能已经被对端
 * 关掉，而这一页回到前台时不该再等下一次事件（见
 * docs/design/events/README.md）。
 *
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
