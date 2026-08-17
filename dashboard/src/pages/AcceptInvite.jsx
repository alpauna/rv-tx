import { useEffect, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { api } from '../api';

export default function AcceptInvite() {
  const [params] = useSearchParams();
  const token = params.get('token') || '';
  const navigate = useNavigate();

  const [info, setInfo] = useState(null);
  const [infoError, setInfoError] = useState(null);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  useEffect(() => {
    if (!token) {
      setInfoError('No invite token in the link.');
      return;
    }
    api.inviteInfo(token).then(setInfo).catch((err) => setInfoError(err.message));
  }, [token]);

  async function onSubmit(e) {
    e.preventDefault();
    if (password !== confirm) {
      setError('Passwords do not match');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.acceptInvite(token, password);
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
      <div className="panel" style={{ width: 340 }}>
        <h2 style={{ marginTop: 0 }}>Accept invite</h2>
        {infoError && <p className="error">{infoError}</p>}
        {!infoError && !info && <p>Loading...</p>}
        {info && !done && (
          <form onSubmit={onSubmit}>
            <p style={{ color: 'var(--text-dim)' }}>
              Setting a password for <strong>{info.email}</strong> ({info.role})
            </p>
            <div className="field">
              <label htmlFor="password">Password</label>
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
        {done && <p>Password set. Redirecting to login...</p>}
      </div>
    </div>
  );
}
