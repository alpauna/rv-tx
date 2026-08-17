import { useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '../api';

export default function ResetPassword() {
  const [params] = useSearchParams();
  const token = params.get('token') || '';
  const navigate = useNavigate();

  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  async function onSubmit(e) {
    e.preventDefault();
    if (password !== confirm) {
      setError('Passwords do not match');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.resetPassword(token, password);
      setDone(true);
      setTimeout(() => navigate('/login'), 1500);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ display: 'flex', minHeight: '100vh', alignItems: 'center', justifyContent: 'center' }}>
      <div className="panel" style={{ width: 320 }}>
        <h2 style={{ marginTop: 0 }}>Reset password</h2>
        {!token && <p className="error">No reset token in the link.</p>}
        {token && !done && (
          <form onSubmit={onSubmit}>
            <div className="field">
              <label htmlFor="password">New password</label>
              <input id="password" type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="confirm">Confirm password</label>
              <input id="confirm" type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />
            </div>
            {error && <p className="error">{error}</p>}
            <button type="submit" disabled={busy || password.length < 8}>Set password</button>
          </form>
        )}
        {done && <p>Password reset. Redirecting to login...</p>}
      </div>
    </div>
  );
}
