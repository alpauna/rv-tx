import { BrowserRouter, Navigate, NavLink, Route, Routes } from 'react-router-dom';
import { AuthProvider, useAuth } from './AuthContext';
import Login from './pages/Login';
import ForgotPassword from './pages/ForgotPassword';
import ResetPassword from './pages/ResetPassword';
import AcceptInvite from './pages/AcceptInvite';
import Nodes from './pages/Nodes';
import Resources from './pages/Resources';
import ConfigPreview from './pages/ConfigPreview';
import Users from './pages/Users';

function Layout({ children }) {
  const { logout, isAdmin } = useAuth();
  return (
    <>
      <header style={{ borderBottom: '1px solid var(--border)', padding: '0.75rem 1.5rem', display: 'flex', alignItems: 'center', gap: '1.5rem' }}>
        <strong>rv-tx</strong>
        <nav style={{ display: 'flex', gap: '1rem', flex: 1 }}>
          <NavLink to="/" end>Nodes</NavLink>
          <NavLink to="/resources">Resources</NavLink>
          <NavLink to="/config">Traefik config</NavLink>
          {isAdmin && <NavLink to="/users">Users</NavLink>}
        </nav>
        <button className="secondary" onClick={logout}>Log out</button>
      </header>
      <main style={{ padding: '1.5rem', flex: 1 }}>{children}</main>
    </>
  );
}

function Protected({ children, adminOnly = false }) {
  const { authed, isAdmin } = useAuth();
  if (authed === null) return null; // still checking the session
  if (!authed) return <Navigate to="/login" replace />;
  if (adminOnly && !isAdmin) return <Layout><p>Admin access required.</p></Layout>;
  return <Layout>{children}</Layout>;
}

function AppRoutes() {
  const { authed } = useAuth();
  return (
    <Routes>
      <Route path="/login" element={authed ? <Navigate to="/" replace /> : <Login />} />
      <Route path="/forgot-password" element={authed ? <Navigate to="/" replace /> : <ForgotPassword />} />
      <Route path="/reset-password" element={authed ? <Navigate to="/" replace /> : <ResetPassword />} />
      <Route path="/accept-invite" element={<AcceptInvite />} />
      <Route path="/" element={<Protected><Nodes /></Protected>} />
      <Route path="/resources" element={<Protected><Resources /></Protected>} />
      <Route path="/config" element={<Protected><ConfigPreview /></Protected>} />
      <Route path="/users" element={<Protected adminOnly><Users /></Protected>} />
    </Routes>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <AuthProvider>
        <AppRoutes />
      </AuthProvider>
    </BrowserRouter>
  );
}
