# rv-tx

A custom HA Traefik-based reverse-proxy/tunnel control plane, replacing Pangolin. Single Go control plane coordinates a WireGuard mesh between nodes and generates Traefik's dynamic configuration for HTTP/TCP/UDP resources.

## Architecture

- `cmd/controlplane` — Postgres-backed WebSocket server: mesh coordination (node registration, IP allocation, peer list distribution) and Traefik dynamic config generation (`GET /traefik/config`), resource management (`POST /resources`).
- `cmd/agent` — runs on each mesh node: manages a local WireGuard interface (`wg`/`ip` directly, not a pure-Go stack), connects to the control plane over WebSocket.
- Traefik itself is used directly as the actual proxy — the control plane's only job is generating its dynamic config, not wrapping/replacing Traefik's own routing model.

Resources can target either a mesh node (`node_name`) or a raw external `ip:port` that never joins the mesh (`address`) — the latter is how the rv-tx.com DNS relay (below) reaches BIND9 without those boxes needing to join the mesh at all.

## Current deployment

- Control plane + one agent (`node-a-pg`): `pangolin-pg` VM, `192.168.1.108`, systemd services `rvtx-controlplane.service` / `rvtx-agent.service`, Postgres database `rvtx`.
- Second agent (`node-b-builder`): `builder`, `165.23.32.123`, systemd service `rvtx-agent.service`.
- DNS relay: Traefik container `rvtx-dns-relay` on `builder` (`--network host`, `--restart unless-stopped`), config at `/opt/rvtx/traefik/traefik.yml`, entrypoints `dns-tcp`/`dns-udp` on real port 53. Relays `rv-tx.com` DNS traffic to the internal BIND9 cluster (dns31/32/33, in the separate `dnsmasq-ui` project) via external-target TCP/UDP resources pointed at its dedicated public-view IPs (`192.168.7.242` primary / `192.168.7.243` backup).

## Milestones

1. Core mesh coordination (WireGuard, Postgres, WebSocket)
2. Traefik config generation for HTTP/TCP resources
3. Endpoint auto-discovery (control plane infers endpoint from connection source + agent-reported port, with a manual override escape hatch)
4. Multi-backend resources — HTTP sticky sessions + healthCheck failover, TCP/UDP master/backup
5. Self-hosted DNS-01 for `rv-tx.com` on the internal BIND9 cluster (in progress — see dnsmasq-ui project's own memory/commits for the BIND9-side half of this work)

## Operational notes / known gotchas

### `builder` needed a routing fix for the DNS relay's UDP replies to work (2026-08-17)

`builder` is dual-homed (`eth0` = LAN `192.168.0.0/23`, `eth1` = WAN, public IP `165.23.32.123`). It shipped with **two default routes at the same DHCP-assigned metric**, one per interface. For any destination not covered by a more-specific route, the kernel's choice between two equal-metric defaults is effectively arbitrary — and this host was picking `eth0` even for genuinely external IPs.

This didn't affect TCP (the kernel pins a TCP reply to the connection's original local address/interface), but it silently broke UDP: a DNS reply to a real external querier would leave via `eth0` with the wrong source IP (`192.168.0.27` instead of `165.23.32.123`), and essentially every DNS client discards a UDP reply whose source IP doesn't match the address it queried — which looks exactly like a timeout, not an error.

**Fix**, in `/etc/netplan/92-eth1-default-priority.yaml`:
```yaml
network:
  version: 2
  ethernets:
    eth1:
      dhcp4-overrides:
        route-metric: 50
      dhcp6-overrides:
        route-metric: 50
    eth0:
      dhcp4-overrides:
        route-metric: 200
      dhcp6-overrides:
        route-metric: 200
      routes:
        - to: 192.168.7.0/24
          via: 192.168.0.1
```

Two parts, both required:
1. `eth1` gets a lower (preferred) default-route metric than `eth0`, so WAN-bound reply traffic reliably picks the right interface instead of an arbitrary tie.
2. A **specific** static route for `192.168.7.0/24` (the mgmt VLAN where BIND9's dedicated public-view IPs live) via `eth0`'s gateway. This subnet had **no route at all** before — it only ever worked by riding whichever default route happened to win the old ambiguous tie, so fixing #1 alone actually broke reachability to the BIND9 boxes until this was added.

**If you add a new subnet `builder` needs to reach** that isn't `192.168.0.0/23` or reachable via the WAN default, don't assume it works just because a similar one does — check `ip route show` for a specific route first, or add one to this same file.

Full incident writeup: `netmonitor_builder_asymmetric_routing` memory (netmonitor project). This is also the most likely real explanation for a previously-unresolved, separate bug (`netmonitor_wireguard_wan_bug`) on this same host — not yet retested against this fix.

### dnsmasq drift can silently break BIND9's TCP:53 (AXFR) fleet-wide

Separate incident, same session, on the BIND9 side (dns31/32/33, `dnsmasq-ui` project): `dnsmasq` is deliberately left enabled at boot (not disabled) for historical reasons, but if it ever starts running again (e.g. after a reboot), it grabs the wildcard `0.0.0.0:53` bind and `named` ends up with **zero TCP listeners at all** — UDP mostly still works (kernel prefers the more-specific bind), but every AXFR (TCP-only) gets silently refused by dnsmasq itself, with zero trace in BIND's own logs. Check `ss -tlnp | grep dnsmasq` on any of the three DNS nodes if AXFR/TCP:53 issues show up again; `systemctl stop dnsmasq && systemctl restart named` is the fix (`named` needs an explicit restart to actually claim the freed TCP socket — stopping dnsmasq alone isn't enough).
