import { useEffect, useState } from 'react';
import { api } from '../api';

// Same freshness heuristic used server-side for TCP/UDP master/backup
// selection -- display-only here, not a decision input.
const ONLINE_THRESHOLD_MS = 45_000;

function isOnline(lastSeen) {
  if (!lastSeen) return false;
  return Date.now() - new Date(lastSeen).getTime() < ONLINE_THRESHOLD_MS;
}

export default function Nodes() {
  const [nodes, setNodes] = useState(null);
  const [error, setError] = useState(null);

  useEffect(() => {
    let cancelled = false;
    function load() {
      api
        .listNodes()
        .then((data) => { if (!cancelled) setNodes(data); })
        .catch((err) => { if (!cancelled) setError(err.message); });
    }
    load();
    const id = setInterval(load, 10_000);
    return () => { cancelled = true; clearInterval(id); };
  }, []);

  return (
    <div className="panel">
      <h2 style={{ marginTop: 0 }}>Nodes</h2>
      {error && <p className="error">{error}</p>}
      {nodes === null && !error && <p>Loading...</p>}
      {nodes && nodes.length === 0 && <p>No nodes have registered yet.</p>}
      {nodes && nodes.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Status</th>
              <th>Name</th>
              <th>Mesh IP</th>
              <th>Last endpoint</th>
              <th>Last seen</th>
            </tr>
          </thead>
          <tbody>
            {nodes.map((n) => {
              const online = isOnline(n.last_seen);
              return (
                <tr key={n.id}>
                  <td>
                    <span className={`dot ${online ? 'online' : 'offline'}`} />
                    {online ? 'online' : 'offline'}
                  </td>
                  <td>{n.name}</td>
                  <td className="mono">{n.mesh_ip}</td>
                  <td className="mono">{n.last_endpoint || '-'}</td>
                  <td className="mono">{n.last_seen ? new Date(n.last_seen).toLocaleString() : '-'}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}
