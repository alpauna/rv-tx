import { useEffect, useState } from 'react';

export default function ConfigPreview() {
  const [config, setConfig] = useState(null);
  const [error, setError] = useState(null);

  function load() {
    // Not under /api -- this is Traefik's own already-public dynamic
    // config endpoint, deliberately unauthenticated (see cmd/controlplane).
    fetch('/traefik/config')
      .then((res) => {
        if (!res.ok) throw new Error(`${res.status} ${res.statusText}`);
        return res.json();
      })
      .then(setConfig)
      .catch((err) => setError(err.message));
  }

  useEffect(() => {
    load();
    const id = setInterval(load, 10_000);
    return () => clearInterval(id);
  }, []);

  return (
    <div className="panel">
      <h2 style={{ marginTop: 0 }}>Traefik config preview</h2>
      <p style={{ color: 'var(--text-dim)', fontSize: '0.85rem' }}>
        Live output of <span className="mono">GET /traefik/config</span> -- exactly what Traefik's
        HTTP provider polls every 2s to build routers, services, and middleware.
      </p>
      {error && <p className="error">{error}</p>}
      {config && (
        <pre className="mono" style={{ overflowX: 'auto', background: 'var(--bg)', padding: '1rem', borderRadius: 8, border: '1px solid var(--border)' }}>
          {JSON.stringify(config, null, 2)}
        </pre>
      )}
    </div>
  );
}
