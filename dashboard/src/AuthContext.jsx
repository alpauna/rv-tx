import { createContext, useContext, useEffect, useState } from 'react';
import { api, ApiError } from './api';

const AuthContext = createContext(null);

// There's no dedicated "am I logged in" endpoint -- the session cookie
// is just checked lazily by whichever real API call runs first. On
// load, probe with a cheap authenticated call (listNodes) so refreshing
// the page doesn't bounce a real session back to the login screen.
export function AuthProvider({ children }) {
  const [authed, setAuthed] = useState(null); // null = still checking

  useEffect(() => {
    api
      .listNodes()
      .then(() => setAuthed(true))
      .catch((err) => setAuthed(!(err instanceof ApiError && err.status === 401)));
  }, []);

  async function login(password) {
    await api.login(password);
    setAuthed(true);
  }

  async function logout() {
    await api.logout().catch(() => {});
    setAuthed(false);
  }

  return (
    <AuthContext.Provider value={{ authed, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth() {
  return useContext(AuthContext);
}
