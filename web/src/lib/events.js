// A subscription to the server's server-sent event stream.
//
// EventSource reconnects on its own, but it reconnects silently: a page that
// has lost the server looks exactly like a page where nothing is happening. So
// the connection state is reported back to the caller, which is what lets the
// UI say "已断开" instead of showing a progress bar that will never move.

const RECONNECT_BASE_MS = 1000
const RECONNECT_MAX_MS = 15000

/**
 * Subscribe to the global event stream.
 *
 * onEvent receives each queue event; onState receives 'open', 'closed', or
 * 'error' so the caller can show whether it is still listening.
 *
 * Returns a function that ends the subscription for good.
 */
export function subscribeEvents(onEvent, onState = () => {}) {
  let source = null
  let retry = RECONNECT_BASE_MS
  let timer = null
  let stopped = false

  function open() {
    if (stopped) return

    source = new EventSource('/api/events')

    source.addEventListener('open', () => {
      retry = RECONNECT_BASE_MS
      onState('open')
    })

    source.addEventListener('progress', (message) => {
      try {
        onEvent(JSON.parse(message.data))
      } catch (error) {
        // A malformed frame is not worth tearing the stream down for; the next
        // one will very likely be fine.
        console.warn('ignoring an unreadable event', error)
      }
    })

    source.addEventListener('error', () => {
      // EventSource would retry on its own, but at a fixed interval and with no
      // way to tell the UI. Closing and retrying here keeps the backoff under
      // our control and makes the state visible.
      onState('error')
      source.close()
      source = null

      if (stopped) return
      timer = setTimeout(open, retry)
      retry = Math.min(retry * 2, RECONNECT_MAX_MS)
    })
  }

  open()

  return function close() {
    stopped = true
    if (timer) clearTimeout(timer)
    if (source) source.close()
    onState('closed')
  }
}
