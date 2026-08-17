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

// Converts an API Target (resolved address, optional node_name) back
// into the form's editable target shape -- node_name is only present
// for a mesh-node-backed target, so its presence (not `external`) is
// what tells the form which mode to preselect.
function targetFromResource(t) {
  return {
    kind: t.node_name ? 'node' : 'external',
    node_name: t.node_name || '',
    address: t.node_name ? '' : t.address,
    port: String(t.port),
    role: t.role || 'primary',
  };
}

function initialFormState(editing) {
  if (!editing) {
    return {
      protocol: 'http', name: '', domain: '', entryPoint: 'websecure', certResolver: '',
      targetScheme: 'http', targetSkipVerify: false, sticky: false, targets: [emptyTarget()],
    };
  }
  return {
    protocol: editing.protocol,
    name: editing.name,
    domain: editing.domain || '',
    entryPoint: editing.entry_point,
    certResolver: editing.cert_resolver || '',
    targetScheme: editing.target_scheme || 'http',
    targetSkipVerify: !!editing.target_skip_verify,
    sticky: !!editing.sticky,
    targets: editing.targets.map(targetFromResource),
  };
}

// Handles both create (editing == null) and edit (editing == the
// resource being changed) -- given a `key` prop keyed on the editing
// target from the parent, React remounts this fresh with the right
// initial state instead of needing a separate effect to resync state
// when switching what's being edited.
function ResourceForm({ nodes, editing, onSaved, onCancel }) {
  const init = initialFormState(editing);
  const [protocol, setProtocol] = useState(init.protocol);
  const [name, setName] = useState(init.name);
  const [domain, setDomain] = useState(init.domain);
  const [entryPoint, setEntryPoint] = useState(init.entryPoint);
  const [certResolver, setCertResolver] = useState(init.certResolver);
  const [targetScheme, setTargetScheme] = useState(init.targetScheme);
  const [targetSkipVerify, setTargetSkipVerify] = useState(init.targetSkipVerify);
  const [sticky, setSticky] = useState(init.sticky);
  const [targets, setTargets] = useState(init.targets);
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
    const payload = {
      name,
      protocol,
      domain: protocol === 'http' ? domain : undefined,
      entry_point: entryPoint,
      cert_resolver: protocol === 'http' && certResolver ? certResolver : undefined,
      target_scheme: protocol === 'http' ? targetScheme : undefined,
      target_skip_verify: protocol === 'http' && targetScheme === 'https' ? targetSkipVerify : undefined,
      sticky: protocol === 'http' ? sticky : undefined,
      targets: targets.map(targetToPayload),
    };
    try {
      if (editing) {
        await api.updateResource(editing.name, payload);
      } else {
        await api.createResource(payload);
        setName('');
        setDomain('');
        setSticky(false);
        setTargetScheme('http');
        setTargetSkipVerify(false);
        setTargets([emptyTarget()]);
      }
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="panel">
      <h3 style={{ marginTop: 0 }}>{editing ? `Edit resource: ${editing.name}` : 'New resource'}</h3>
      <div className="row">
        <div className="field">
          <label htmlFor="r-protocol">Protocol</label>
          <select id="r-protocol" value={protocol} onChange={(e) => setProtocol(e.target.value)}>
            {PROTOCOLS.map((p) => <option key={p} value={p}>{p}</option>)}
          </select>
        </div>
        <div className="field">
          <label htmlFor="r-name">Name</label>
          <input id="r-name" value={name} onChange={(e) => setName(e.target.value)} required disabled={!!editing} title={editing ? 'Renaming isn\'t supported here -- delete and recreate instead.' : undefined} />
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

      {protocol === 'http' && (
        <div className="row">
          <div className="field">
            <label htmlFor="r-target-scheme">Backend scheme</label>
            <select id="r-target-scheme" value={targetScheme} onChange={(e) => setTargetScheme(e.target.value)}>
              <option value="http">http</option>
              <option value="https">https</option>
            </select>
          </div>
          {targetScheme === 'https' && (
            <div className="field">
              <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}>
                <input
                  type="checkbox"
                  checked={targetSkipVerify}
                  onChange={(e) => setTargetSkipVerify(e.target.checked)}
                />
                Skip TLS verification (self-signed backend cert)
              </label>
            </div>
          )}
          <div className="field">
            <label
              style={{ display: 'flex', alignItems: 'center', gap: '0.4rem' }}
              title="Pins each client to whichever backend it lands on first, via a cookie. Useful for multi-target pools where a target keeps per-node session state -- e.g. Proxmox's own web UI ties its auth ticket to whichever node issued it."
            >
              <input
                type="checkbox"
                checked={sticky}
                onChange={(e) => setSticky(e.target.checked)}
              />
              Sticky sessions
            </label>
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
      <div className="row">
        <button type="submit" disabled={busy}>{editing ? 'Save changes' : 'Create resource'}</button>
        {editing && (
          <button type="button" className="secondary" onClick={onCancel}>Cancel</button>
        )}
      </div>
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
  const [editing, setEditing] = useState(null);

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
      if (editing && editing.name === name) setEditing(null);
      load();
    } catch (err) {
      setError(err.message);
    }
  }

  function onSaved() {
    setEditing(null);
    load();
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
                    {r.targets
                      .map((t) => {
                        const prefix = r.protocol === 'http' ? `${r.target_scheme || 'http'}://` : '';
                        return `${prefix}${t.address}:${t.port}${t.role ? ` (${t.role})` : ''}`;
                      })
                      .join(', ')}
                    {r.protocol === 'http' && r.target_scheme === 'https' && r.target_skip_verify && (
                      <span style={{ color: 'var(--text-dim)' }}> (skip verify)</span>
                    )}
                    {r.protocol === 'http' && r.sticky && (
                      <span style={{ color: 'var(--text-dim)' }}> (sticky)</span>
                    )}
                  </td>
                  {isAdmin && (
                    <td style={{ display: 'flex', gap: '0.5rem' }}>
                      <button className="secondary" onClick={() => setEditing(r)}>Edit</button>
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
          <ResourceForm
            key={editing ? editing.name : 'new'}
            nodes={nodes}
            editing={editing}
            onSaved={onSaved}
            onCancel={() => setEditing(null)}
          />

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
