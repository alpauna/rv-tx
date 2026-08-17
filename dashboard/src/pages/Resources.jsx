import { useEffect, useState } from 'react';
import { useAuth } from '../AuthContext';
import { api } from '../api';

const PROTOCOLS = ['http', 'tcp', 'udp'];

function emptyTarget() {
  return { kind: 'node', node_name: '', address: '', port: '', role: 'primary' };
}

function targetToPayload(t) {
  const payload = { port: Number(t.port) };
  if (t.kind === 'node') payload.node_name = t.node_name;
  else payload.address = t.address;
  if (t.role) payload.role = t.role;
  return payload;
}

function NewResourceForm({ nodes, onCreated }) {
  const [protocol, setProtocol] = useState('http');
  const [name, setName] = useState('');
  const [domain, setDomain] = useState('');
  const [entryPoint, setEntryPoint] = useState('websecure');
  const [certResolver, setCertResolver] = useState('');
  const [targets, setTargets] = useState([emptyTarget()]);
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);

  function updateTarget(i, patch) {
    setTargets((ts) => ts.map((t, idx) => (idx === i ? { ...t, ...patch } : t)));
  }

  function addTarget() {
    setTargets((ts) => [...ts, emptyTarget()]);
  }

  function removeTarget(i) {
    setTargets((ts) => ts.filter((_, idx) => idx !== i));
  }

  async function onSubmit(e) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await api.createResource({
        name,
        protocol,
        domain: protocol === 'http' ? domain : undefined,
        entry_point: entryPoint,
        cert_resolver: protocol === 'http' && certResolver ? certResolver : undefined,
        targets: targets.map(targetToPayload),
      });
      setName('');
      setDomain('');
      setTargets([emptyTarget()]);
      onCreated();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="panel">
      <h3 style={{ marginTop: 0 }}>New resource</h3>
      <div className="row">
        <div className="field">
          <label htmlFor="r-protocol">Protocol</label>
          <select id="r-protocol" value={protocol} onChange={(e) => setProtocol(e.target.value)}>
            {PROTOCOLS.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
        <div className="field">
          <label htmlFor="r-name">Name</label>
          <input id="r-name" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="r-entrypoint">Entry point</label>
          <input id="r-entrypoint" value={entryPoint} onChange={(e) => setEntryPoint(e.target.value)} required />
        </div>
      </div>

      {protocol === 'http' && (
        <div className="row">
          <div className="field">
            <label htmlFor="r-domain">Domain (leading "*." for a wildcard)</label>
            <input id="r-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.rv-tx.com or *.rv-tx.com" />
          </div>
          <div className="field">
            <label htmlFor="r-certresolver">Cert resolver</label>
            <select id="r-certresolver" value={certResolver} onChange={(e) => setCertResolver(e.target.value)}>
              <option value="">none (no TLS)</option>
              <option value="letsencrypt-staging">letsencrypt-staging</option>
              <option value="letsencrypt">letsencrypt</option>
            </select>
          </div>
        </div>
      )}

      <div className="field">
        <label>Targets</label>
        {targets.map((t, i) => (
          <div className="target-row" key={i}>
            <select value={t.kind} onChange={(e) => updateTarget(i, { kind: e.target.value })}>
              <option value="node">mesh node</option>
              <option value="external">external address</option>
            </select>
            {t.kind === 'node' ? (
              <select value={t.node_name} onChange={(e) => updateTarget(i, { node_name: e.target.value })} required>
                <option value="" disabled>select node</option>
                {nodes.map((n) => <option key={n.name} value={n.name}>{n.name}</option>)}
              </select>
            ) : (
              <input placeholder="address" value={t.address} onChange={(e) => updateTarget(i, { address: e.target.value })} required />
            )}
            <input type="number" placeholder="port" style={{ width: 90 }} value={t.port} onChange={(e) => updateTarget(i, { port: e.target.value })} required />
            {protocol !== 'http' && (
              <select value={t.role} onChange={(e) => updateTarget(i, { role: e.target.value })}>
                <option value="primary">primary</option>
                <option value="backup">backup</option>
              </select>
            )}
            {targets.length > 1 && (
              <button type="button" className="secondary" onClick={() => removeTarget(i)}>Remove</button>
            )}
          </div>
        ))}
        <button type="button" className="secondary" onClick={addTarget}>Add target</button>
      </div>

      {error && <p className="error">{error}</p>}
      <button type="submit" disabled={busy}>Create resource</button>
    </form>
  );
}

function DnsNameserverWizard({ onCreated }) {
  const [name, setName] = useState('');
  const [primary, setPrimary] = useState('');
  const [backup, setBackup] = useState('');
  const [port, setPort] = useState('53');
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const targets = [{ address: primary, port: Number(port), role: 'primary' }];
      if (backup) targets.push({ address: backup, port: Number(port), role: 'backup' });

      await api.createResource({ name: `${name}-tcp`, protocol: 'tcp', entry_point: 'dns-tcp', targets });
      await api.createResource({ name: `${name}-udp`, protocol: 'udp', entry_point: 'dns-udp', targets });

      setName('');
      setPrimary('');
      setBackup('');
      onCreated();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="panel">
      <h3 style={{ marginTop: 0 }}>Add DNS nameserver</h3>
      <p style={{ color: 'var(--text-dim)', fontSize: '0.85rem', marginTop: 0 }}>
        Creates matching TCP + UDP port-53 relay resources (the pattern behind ns1/ns2.rv-tx.com).
        This only wires the relay -- the registrar side (adding the nameserver at Epik, glue
        records, the 2-nameserver minimum) still has to be done there by hand.
      </p>
      <div className="row">
        <div className="field">
          <label htmlFor="dns-name">Base name</label>
          <input id="dns-name" placeholder="ns3-rv-tx-com" value={name} onChange={(e) => setName(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="dns-port">Port</label>
          <input id="dns-port" type="number" style={{ width: 90 }} value={port} onChange={(e) => setPort(e.target.value)} required />
        </div>
      </div>
      <div className="row">
        <div className="field">
          <label htmlFor="dns-primary">Primary address</label>
          <input id="dns-primary" value={primary} onChange={(e) => setPrimary(e.target.value)} required />
        </div>
        <div className="field">
          <label htmlFor="dns-backup">Backup address (optional)</label>
          <input id="dns-backup" value={backup} onChange={(e) => setBackup(e.target.value)} />
        </div>
      </div>
      {error && <p className="error">{error}</p>}
      <button type="submit" disabled={busy}>Create TCP + UDP resources</button>
    </form>
  );
}

export default function Resources() {
  const { isAdmin } = useAuth();
  const [resources, setResources] = useState(null);
  const [nodes, setNodes] = useState([]);
  const [error, setError] = useState(null);
  const [showWizard, setShowWizard] = useState(false);

  function load() {
    api.listResources().then(setResources).catch((err) => setError(err.message));
  }

  useEffect(() => {
    load();
    api.listNodes().then(setNodes).catch(() => {});
  }, []);

  async function onDelete(name) {
    if (!confirm(`Delete resource "${name}"?`)) return;
    try {
      await api.deleteResource(name);
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <div>
      <div className="panel">
        <h2 style={{ marginTop: 0 }}>Resources</h2>
        {error && <p className="error">{error}</p>}
        {resources === null && !error && <p>Loading...</p>}
        {resources && resources.length === 0 && <p>No resources yet.</p>}
        {resources && resources.length > 0 && (
          <table>
            <thead>
              <tr>
                <th>Name</th>
                <th>Protocol</th>
                <th>Domain / entry point</th>
                <th>Cert resolver</th>
                <th>Targets</th>
                {isAdmin && <th></th>}
              </tr>
            </thead>
            <tbody>
              {resources.map((r) => (
                <tr key={r.name}>
                  <td>{r.name}</td>
                  <td>{r.protocol}</td>
                  <td className="mono">{r.domain || r.entry_point}</td>
                  <td>{r.cert_resolver || '-'}</td>
                  <td className="mono">
                    {r.targets.map((t) => `${t.address}:${t.port}${t.role ? ` (${t.role})` : ''}`).join(', ')}
                  </td>
                  {isAdmin && (
                    <td>
                      <button className="danger" onClick={() => onDelete(r.name)}>Delete</button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {isAdmin && (
        <>
          <NewResourceForm nodes={nodes} onCreated={load} />

          <div className="panel">
            <button type="button" className="secondary" onClick={() => setShowWizard((v) => !v)}>
              {showWizard ? 'Hide DNS nameserver wizard' : 'Add DNS nameserver...'}
            </button>
          </div>
          {showWizard && <DnsNameserverWizard onCreated={load} />}
        </>
      )}
    </div>
  );
}
