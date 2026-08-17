import { createContext, useContext, useEffect, useState } from 'react';
import { api, ApiError } from './api';

const AuthContext = createContext(null);

// user is null while checking, false when definitely logged out, or
// {email, role} once whoami succeeds -- refreshing the page doesn't
// bounce a real session back to the login screen.
export function AuthProvider({ children }) {
  const [user, setUser] = useState(null);

  useEffect(() => {
    api
      .whoami()
      .then((u) => setUser(u))
      .catch((err) => setUser(err instanceof ApiError && err.status === 401 ? false : false));
  }, []);

  async function login(email, password) {
    await api.login(email, password);
    const u = await api.whoami();
    setUser(u);
  }

  async function logout() {
    await api.logout().catch(() => {});
    setUser(false);
  }

  const authed = user === null ? null : user !== false;
  const isAdmin = !!user && user.role === 'admin';

  return (
    <AuthContext.Provider value={{ user, authed, isAdmin, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  return useContext(AuthContext);
}
