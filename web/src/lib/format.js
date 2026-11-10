// Presentation helpers. Pure functions: they take values and return strings,
// with nothing to configure or remember between calls.

/** A duration in milliseconds as m:ss. */
export function duration(ms) {
  if (!ms || ms < 0) return '—'
  const total = Math.round(ms / 1000)
  return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}

/** A byte count in binary units. */
export function bytes(n) {
  if (!n) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = n
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`
}

/** How long ago an ISO timestamp was. */
export function ago(iso) {
  if (!iso) return ''
  const seconds = (Date.now() - new Date(iso).getTime()) / 1000
  if (seconds < 60) return '刚刚'
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`
  return `${Math.floor(seconds / 86400)} 天前`
}

const PHASES = {
  queued: '排队中',
  loading_model: '加载模型',
  generating: '生成中',
  decoding: '编码音频',
  done: '完成',
}

/** A phase name in the interface language. */
export function phase(name) {
  return PHASES[name] ?? name ?? ''
}

const STATES = {
  pending: '排队中',
  running: '进行中',
  succeeded: '已完成',
  failed: '失败',
  canceled: '已取消',
  interrupted: '已中断',
}

/** A job state in the interface language. */
export function state(name) {
  return STATES[name] ?? name ?? ''
}
