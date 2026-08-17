import { useEffect, useState } from 'react';
import { useAuth } from '../AuthContext';
import { api } from '../api';

export default function Users() {
  const { user: me } = useAuth();
  const [users, setUsers] = useState(null);
  const [error, setError] = useState(null);
  const [email, setEmail] = useState('');
  const [role, setRole] = useState('viewer');
  const [busy, setBusy] = useState(false);

  function load() {
    api.listUsers().then(setUsers).catch((err) => setError(err.message));
  }

  useEffect(load, []);

  async function onInvite(e) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.inviteUser(email, role);
      setEmail('');
      setRole('viewer');
      load();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  async function onDelete(targetEmail) {
    if (!confirm(`Remove user "${targetEmail}"?`)) return;
    try {
      await api.deleteUser(targetEmail);
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  async function onRoleChange(targetEmail, newRole) {
    try {
      await api.setUserRole(targetEmail, newRole);
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <div>
      <div className="panel">
        <h2 style={{ marginTop: 0 }}>Users</h2>
        {error && <p className="error">{error}</p>}
        {users === null && !error && <p>Loading...</p>}
        {users && (
          <table>
            <thead>
              <tr>
                <th>Email</th>
                <th>Role</th>
                <th>Status</th>
                <th>Invited</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {users.map((u) => {
                const self = u.email === me?.email;
                return (
                  <tr key={u.email}>
                    <td>{u.email}</td>
                    <td>
                      <select
                        value={u.role}
                        disabled={self}
                        onChange={(e) => onRoleChange(u.email, e.target.value)}
                      >
                        <option value="admin">admin</option>
                        <option value="viewer">viewer</option>
                      </select>
                    </td>
                    <td>{u.has_set_password ? 'active' : 'invite pending'}</td>
                    <td className="mono">{new Date(u.created_at).toLocaleDateString()}</td>
                    <td>
                      <button className="danger" disabled={self} onClick={() => onDelete(u.email)}>Remove</button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <form onSubmit={onInvite} className="panel">
        <h3 style={{ marginTop: 0 }}>Invite a user</h3>
        <div className="row">
          <div className="field">
            <label htmlFor="invite-email">Email</label>
            <input id="invite-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor="invite-role">Role</label>
            <select id="invite-role" value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="viewer">viewer</option>
              <option value="admin">admin</option>
            </select>
          </div>
        </div>
        <button type="submit" disabled={busy || !email}>Send invite</button>
      </form>
    </div>
  );
}
