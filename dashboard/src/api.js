// Small fetch wrapper for the control plane's /api/* endpoints.
// Session auth is a plain cookie (see internal/controlplane/auth),
// so every request just needs credentials included -- no token
// handling on the client at all.

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

async function request(path, options = {}) {
  const res = await fetch(`/api${path}`, {
    ...options,
    credentials: 'include',
    headers: {
      ...(options.body ? { 'Content-Type': 'application/json' } : {}),
      ...options.headers,
    },
  });

  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new ApiError(res.status, text || `${res.status} ${res.statusText}`);
  }

  if (res.status === 204) return null;
  const contentType = res.headers.get('content-type') || '';
  if (contentType.includes('application/json')) return res.json();
  return null;
}

export const api = {
  login: (email, password) => request('/login', { method: 'POST', body: JSON.stringify({ email, password }) }),
  logout: () => request('/logout', { method: 'POST' }),
  whoami: () => request('/whoami'),
  listNodes: () => request('/nodes'),
  listResources: () => request('/resources'),
  createResource: (resource) => request('/resources', { method: 'POST', body: JSON.stringify(resource) }),
  updateResource: (name, resource) => request(`/resources/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify(resource) }),
  deleteResource: (name) => request(`/resources/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  listUsers: () => request('/users'),
  inviteUser: (email, role) => request('/users', { method: 'POST', body: JSON.stringify({ email, role }) }),
  deleteUser: (email) => request(`/users/${encodeURIComponent(email)}`, { method: 'DELETE' }),
  setUserRole: (email, role) => request(`/users/${encodeURIComponent(email)}/role`, { method: 'PUT', body: JSON.stringify({ role }) }),
  inviteInfo: (token) => request(`/invite-info?token=${encodeURIComponent(token)}`),
  acceptInvite: (token, password) => request('/accept-invite', { method: 'POST', body: JSON.stringify({ token, password }) }),
  forgotPassword: (email) => request('/forgot-password', { method: 'POST', body: JSON.stringify({ email }) }),
  resetPassword: (token, password) => request('/reset-password', { method: 'POST', body: JSON.stringify({ token, password }) }),
};

export { ApiError };
