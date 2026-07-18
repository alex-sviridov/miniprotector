import { useAuthStore } from '../stores/auth'

const BASE_URL = '/api/v1'

export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

export async function apiFetch(path, options = {}) {
  const auth = useAuthStore()
  const headers = { ...(options.headers || {}) }
  if (auth.token) {
    headers.Authorization = `Bearer ${auth.token}`
  }

  const response = await fetch(`${BASE_URL}${path}`, { ...options, headers })

  if (response.status === 401) {
    auth.clearToken('Invalid or expired token — please re-enter it.')
  }

  if (!response.ok) {
    let message = `Request failed with status ${response.status}`
    try {
      const body = await response.json()
      if (body && body.error) message = body.error
    } catch {
      // non-JSON error body; keep the default message
    }
    throw new ApiError(response.status, message)
  }

  if (response.status === 204) {
    return null
  }

  return response.json()
}
