// The HTTP client for the local API.
//
// Every call goes to a relative /api path: in the built binary the UI and the
// API share an origin, and the dev server proxies /api, so there is never a
// host to configure.

// ApiError carries the server's message and status, so a caller can show what
// actually went wrong rather than "request failed".
export class ApiError extends Error {
  constructor(message, status) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

async function request(path, options = {}) {
  let response
  try {
    response = await fetch(path, {
      headers: options.body ? { 'Content-Type': 'application/json' } : undefined,
      ...options,
    })
  } catch (cause) {
    // A failed fetch means the server is not there, which for a local app is
    // the most likely failure and deserves a plain explanation.
    throw new ApiError('无法连接到 jonus 服务，请确认 jonus serve 正在运行', 0, { cause })
  }

  if (response.status === 204) {
    return null
  }

  const text = await response.text()
  let body = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      // A non-JSON body from an /api path means something is badly wrong; say
      // so instead of pretending it is data.
      throw new ApiError(`服务返回了非 JSON 响应（HTTP ${response.status}）`, response.status)
    }
  }

  if (!response.ok) {
    throw new ApiError(body?.error ?? `HTTP ${response.status}`, response.status)
  }
  return body
}

export const api = {
  engineStatus: () => request('/api/engine/status'),

  listJobs: (limit = 50) => request(`/api/jobs?limit=${limit}`),

  getJob: (id) => request(`/api/jobs/${encodeURIComponent(id)}`),

  createJob: (job) =>
    request('/api/jobs', {
      method: 'POST',
      body: JSON.stringify(job),
    }),

  cancelJob: (id) => request(`/api/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),

  retryJob: (id) => request(`/api/jobs/${encodeURIComponent(id)}/retry`, { method: 'POST' }),

  deleteJob: (id) => request(`/api/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' }),

  listTracks: (limit = 100) => request(`/api/tracks?limit=${limit}`),
}
