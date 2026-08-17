import { useState } from 'react';
import { Link } from 'react-router-dom';
import { api } from '../api';

export default function ForgotPassword() {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState(null);

  async function onSubmit(e) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.forgotPassword(email);
      setSent(true);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div style={{ display: 'flex', minHeight: '100vh', alignItems: 'center', justifyContent: 'center' }}>
      <div className="panel" style={{ width: 320 }}>
        <h2 style={{ marginTop: 0 }}>Forgot password</h2>
        {sent ? (
          <p>If that email has an account, a reset link is on its way.</p>
        ) : (
          <form onSubmit={onSubmit}>
            <div className="field">
              <label htmlFor="email">Email</label>
              <input id="email" type="email" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            {error && <p className="error">{error}</p>}
            <button type="submit" disabled={busy || !email}>Send reset link</button>
          </form>
        )}
        <p style={{ marginBottom: 0, marginTop: '1rem' }}>
          <Link to="/login">Back to login</Link>
        </p>
      </div>
    </div>
  );
}
